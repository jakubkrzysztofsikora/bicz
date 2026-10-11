package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// LossResult is the receipt of a finalized burn.
type LossResult struct {
	EventID             string
	AccountID           string
	Rule                string
	RequestedAmount     int64
	FormulaAmount       int64
	AppliedAmount       int64
	RepeatCount         int64
	RepeatMultiplier    int64
	GuardBand           bool
	SelfDisclosed       bool
	BalanceBefore       int64
	BalanceAfter        int64
	HeldRevoked         int64
	RevokedReservations []string
	LessonID            int64
	OutstandingLoss     int64
	Role                string
	Warnings            int64
	Replayed            bool
	// Disposition is the stated classification of the burn. Cue is the
	// accountability statement of a confirmed penalty and is empty for every
	// other disposition; on a replay it is the original, not a new cue.
	Disposition Disposition
	Cue         string
}

type lossConfig struct {
	selfDisclosed bool
	disposition   Disposition
}

// LossOption tunes a single RecordLoss call.
type LossOption func(*lossConfig)

// SelfDisclosed marks the incident as disclosed by the agent before detection.
// A self-disclosed incident is priced below detection on purpose
// (docs/PRESSURE_MECHANICS.md §5): it is recorded and burned, but it never increments
// the repeat count and never escalates the role, so honesty stays the cheapest
// strategy.
func SelfDisclosed() LossOption {
	return func(cfg *lossConfig) { cfg.selfDisclosed = true }
}

// RecordLoss finalizes a burn for accountID.
//
// The amount is computed from the frozen formula in docs/CONTRACTS.md §6.1 —
// never by a model verdict and never by operator tone — and is capped by the
// capital that actually exists:
//
//	L    = min(Lmax, max(floor, ceil(rho * K)) * m(n))
//	burn = min(requested, L, K)          and never more than K − frozen holds
//
// All of it happens in one transaction: the burn, the revocation of the account's
// `held` reservations (their hold is released — `orphaned` holds are retained,
// docs/CONTRACTS.md §1 I8), the durable `lessons` row, and the escalating `role`
// event. The lesson cannot exist without the loss and the loss cannot exist
// without the lesson.
//
// Requirements the contract makes non-negotiable: a non-empty rule AND a
// non-empty correction. Everything else — scope, exceptions, evidence — is
// recorded verbatim.
func (l *Ledger) RecordLoss(
	ctx context.Context,
	eventID, accountID string,
	requestedAmount int64,
	rule, correction, scope, exceptions, evidence, policyVersion string,
	opts ...LossOption,
) (LossResult, error) {
	cfg := lossConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if _, err := ParseDisposition(string(cfg.disposition)); err != nil {
		return LossResult{}, err
	}
	if eventID == "" {
		return LossResult{}, errf(ErrInvalidArgument, "loss event id is empty")
	}
	if accountID == "" {
		return LossResult{}, errf(ErrUnknownIdentity, "loss account id is empty")
	}
	if rule == "" {
		return LossResult{}, errf(ErrRuleRequired, "loss %q must name the rule it enforces", eventID)
	}
	if correction == "" {
		return LossResult{}, errf(ErrCorrectionRequired, "loss %q must state the required correction", eventID)
	}
	if evidence == "" {
		return LossResult{}, errf(ErrReasonRequired, "loss %q requires evidence", eventID)
	}
	if policyVersion == "" {
		return LossResult{}, errf(ErrUnknownPolicyVersion, "loss %q requires a policy version", eventID)
	}
	if requestedAmount < 0 {
		return LossResult{}, errf(ErrInvalidAmount, "requested loss %d", requestedAmount)
	}

	var result LossResult
	err := l.mutate(ctx, func(tx *sql.Tx) error {
		stored, found, err := loadEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if found {
			return replayLoss(ctx, tx, stored, lossReplay{
				accountID:       accountID,
				rule:            rule,
				correction:      correction,
				scope:           scope,
				exceptions:      exceptions,
				policyVersion:   policyVersion,
				requestedAmount: requestedAmount,
				selfDisclosed:   cfg.selfDisclosed,
				disposition:     cfg.disposition,
			}, &result)
		}

		account, found, err := loadAccount(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return errf(ErrUnknownIdentity, "%q", accountID)
		}
		employment, err := loadEmployment(ctx, tx, accountID)
		if err != nil {
			return err
		}
		policy, err := loadPolicy(ctx, tx, policyVersion)
		if err != nil {
			return err
		}
		repeats, err := confirmedRepeats(ctx, tx, accountID, rule, policyVersion)
		if err != nil {
			return err
		}
		formula, multiplier, band := policy.formula(rule, account.Balance, repeats)

		// A burn touches earned capital only, and the accounts CHECK keeps
		// held <= balance: release the live holds first, then cap the burn at the
		// credit that is genuinely free.
		revoked, released, err := releaseHeld(ctx, tx, accountID, StateRevoked)
		if err != nil {
			return err
		}
		sort.Strings(revoked)

		applied := formula
		if requestedAmount > 0 {
			applied = minInt64(requestedAmount, formula)
		}
		// A burn may only consume credit that is genuinely free: what is left
		// after the retained (orphaned) holds, which are never released by a
		// penalty (docs/CONTRACTS.md §1 I8). Burning into them would violate the
		// accounts CHECK held <= balance.
		free := maxInt64(account.Balance-account.Held+released, 0)
		applied = maxInt64(minInt64(applied, account.Balance, free), 0)

		before, err := unrecovered(ctx, tx, accountID)
		if err != nil {
			return err
		}
		// The burn is a new loss entry whose remainder is exactly `applied`, so
		// the shortfall after it is arithmetically exact.
		outstandingAfter := before.Outstanding + applied

		var lessonID int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) + 1 FROM lessons`).Scan(&lessonID); err != nil {
			return fmt.Errorf("ledger: next lesson id: %w", err)
		}

		if applied > 0 {
			result, err := tx.ExecContext(ctx,
				`UPDATE accounts SET balance = balance - ? WHERE id = ?`, applied, accountID)
			if err != nil {
				return fmt.Errorf("ledger: burn for %s: %w", accountID, err)
			}
			if affected, err := result.RowsAffected(); err != nil || affected != 1 {
				return errf(ErrUnknownIdentity, "%q", accountID)
			}
		}

		warnings := employment.Warnings
		role := employment.State
		if !cfg.selfDisclosed {
			warnings++
			if role == RoleActive {
				role = RoleWarned
			}
			if err := setEmployment(ctx, tx, accountID, Employment{State: role, Warnings: warnings}); err != nil {
				return err
			}
		}

		payload := lossPayload{
			AccountID:             accountID,
			Rule:                  rule,
			Correction:            correction,
			Scope:                 scope,
			Exceptions:            exceptions,
			PolicyVersion:         policyVersion,
			RequestedAmount:       requestedAmount,
			FormulaAmount:         formula,
			AppliedAmount:         applied,
			RepeatCount:           repeats,
			RepeatMultiplier:      multiplier,
			GuardBand:             band,
			SelfDisclosed:         cfg.selfDisclosed,
			Disposition:           string(cfg.disposition),
			ClassificationVersion: classificationVersion,
			BalanceBefore:         account.Balance,
			BalanceAfter:          account.Balance - applied,
			HeldBefore:            account.Held,
			HeldAfter:             account.Held - released,
			Revoked:               int64(len(revoked)),
			RevokedReservations:   revoked,
			LessonID:              lessonID,
			OutstandingAfter:      outstandingAfter,
		}
		if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
			id:        eventID,
			accountID: &accountID,
			kind:      kindLoss,
			amount:    -applied,
			payload:   payload,
			actor:     l.actor,
			reason:    evidence,
		}); err != nil {
			return err
		}
		// lessons.event_id REFERENCES events(id), so the event is written first;
		// the id is reserved above so the payload can name it before it exists.
		if _, err := tx.ExecContext(ctx, `INSERT INTO lessons
			(id, event_id, account_id, rule, correction, scope, exceptions, evidence, policy_version, at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lessonID, eventID, accountID, rule, correction, scope, exceptions, evidence,
			policyVersion, l.clock.Timestamp()); err != nil {
			return fmt.Errorf("ledger: write lesson for %s: %w", eventID, err)
		}
		if len(revoked) > 0 {
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        "revoke:loss:" + eventID,
				accountID: &accountID,
				kind:      kindSpend,
				amount:    0,
				payload: revokePayload{
					Phase:         "revoke_all",
					AccountID:     accountID,
					Released:      released,
					Reservations:  revoked,
					SourceEventID: eventID,
				},
				actor:  l.actor,
				reason: "penalty revocation: " + evidence,
			}); err != nil {
				return err
			}
		}
		if !cfg.selfDisclosed && role != employment.State {
			if _, _, err := appendEvent(ctx, tx, l.clock, eventSpec{
				id:        "role:loss:" + eventID,
				accountID: &accountID,
				kind:      kindRole,
				amount:    0,
				payload: rolePayload{
					AccountID: accountID,
					Previous:  employment.State,
					State:     role,
					Warnings:  warnings,
				},
				actor:  l.actor,
				reason: "escalation: " + evidence,
			}); err != nil {
				return err
			}
		}
		result = LossResult{
			EventID:             eventID,
			AccountID:           accountID,
			Rule:                rule,
			RequestedAmount:     requestedAmount,
			FormulaAmount:       formula,
			AppliedAmount:       applied,
			RepeatCount:         repeats,
			RepeatMultiplier:    multiplier,
			GuardBand:           band,
			SelfDisclosed:       cfg.selfDisclosed,
			BalanceBefore:       account.Balance,
			BalanceAfter:        account.Balance - applied,
			HeldRevoked:         int64(len(revoked)),
			RevokedReservations: revoked,
			LessonID:            lessonID,
			OutstandingLoss:     outstandingAfter,
			Role:                role,
			Warnings:            warnings,
			Disposition:         cfg.disposition,
		}
		if payload.accountable() {
			result.Cue = penaltyCue(rule, correction, evidence, applied, applied, eventID, cfg.selfDisclosed)
		}
		return nil
	})
	if err != nil {
		return LossResult{}, err
	}
	return result, nil
}

type lossReplay struct {
	accountID       string
	rule            string
	correction      string
	scope           string
	exceptions      string
	policyVersion   string
	requestedAmount int64
	selfDisclosed   bool
	disposition     Disposition
}

// replayLoss returns the prior result of a loss event, refusing a replay whose
// defining fields differ (docs/CONTRACTS.md §2, I10).
func replayLoss(ctx context.Context, tx *sql.Tx, stored Event, replay lossReplay, out *LossResult) error {
	if stored.Kind != kindLoss {
		return errf(ErrPayloadConflict, "event %q is recorded as kind %q, not %q", stored.ID, stored.Kind, kindLoss)
	}
	var payload lossPayload
	if err := decodePayload(stored, &payload); err != nil {
		return err
	}
	if payload.AccountID != replay.accountID || payload.Rule != replay.rule ||
		payload.Correction != replay.correction || payload.Scope != replay.scope ||
		payload.Exceptions != replay.exceptions || payload.PolicyVersion != replay.policyVersion ||
		payload.RequestedAmount != replay.requestedAmount || payload.SelfDisclosed != replay.selfDisclosed ||
		payload.Disposition != string(replay.disposition) {
		return errf(ErrPayloadConflict, "loss %q replayed with a different payload", stored.ID)
	}
	employment, err := loadEmployment(ctx, tx, replay.accountID)
	if err != nil {
		return err
	}
	state, err := unrecovered(ctx, tx, replay.accountID)
	if err != nil {
		return err
	}
	*out = LossResult{
		EventID:             stored.ID,
		AccountID:           payload.AccountID,
		Rule:                payload.Rule,
		RequestedAmount:     payload.RequestedAmount,
		FormulaAmount:       payload.FormulaAmount,
		AppliedAmount:       payload.AppliedAmount,
		RepeatCount:         payload.RepeatCount,
		RepeatMultiplier:    payload.RepeatMultiplier,
		GuardBand:           payload.GuardBand,
		SelfDisclosed:       payload.SelfDisclosed,
		BalanceBefore:       payload.BalanceBefore,
		BalanceAfter:        payload.BalanceAfter,
		HeldRevoked:         payload.Revoked,
		RevokedReservations: payload.RevokedReservations,
		LessonID:            payload.LessonID,
		OutstandingLoss:     state.Outstanding,
		Role:                employment.State,
		Warnings:            employment.Warnings,
		Replayed:            true,
		Disposition:         payload.effectiveDisposition(),
	}
	if payload.accountable() {
		out.Cue = penaltyCue(payload.Rule, payload.Correction, stored.Reason, payload.AppliedAmount,
			state.Remaining[stored.ID], stored.ID, payload.SelfDisclosed)
	}
	return nil
}

// confirmedRepeats counts prior, non-self-disclosed losses for the same rule
// under the same policy version. Reconsidered verdicts and self-disclosures do
// not count as repetitions (docs/CONTRACTS.md §6.1), so m(n) starts at 1 for the
// first occurrence.
func confirmedRepeats(ctx context.Context, q querier, accountID, rule, policyVersion string) (int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT payload FROM events WHERE account_id = ? AND kind = ? ORDER BY seq ASC`,
		accountID, kindLoss)
	if err != nil {
		return 0, fmt.Errorf("ledger: read prior losses for %s: %w", accountID, err)
	}
	defer rows.Close()
	var repeats int64
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return 0, fmt.Errorf("ledger: scan prior loss: %w", err)
		}
		var decoded lossPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return 0, errw(ErrPayloadConflict, fmt.Errorf("prior loss payload: %w", err))
		}
		if decoded.Rule == rule && decoded.PolicyVersion == policyVersion && !decoded.SelfDisclosed {
			repeats++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("ledger: read prior losses for %s: %w", accountID, err)
	}
	return repeats, nil
}

// lossState is the relief/restoration ledger of docs/CONTRACTS.md §6.2: how much
// has been burned, how much of it has been recovered, and which burn is still
// outstanding.
type lossState struct {
	Burned       int64
	Recovered    int64
	Outstanding  int64
	Target       string           // earliest loss event with an unrecovered remainder
	TargetBurned int64            // that loss event's original burn
	Remaining    map[string]int64 // per-loss-event unrecovered remainder
}

type recoveryEntry struct {
	id        string
	original  int64
	remaining int64
}

// unrecovered replays the account's loss and recovery events in seq order and
// returns the outstanding shortfall. A reward's applied_to_loss repays burns
// oldest-first; a compensation referencing a loss repays that specific burn.
// The outstanding counter is what makes a later reward read as recovery of a
// specific prior burn rather than as ambient income (docs/PRESSURE_MECHANICS.md §4).
func unrecovered(ctx context.Context, q querier, accountID string) (lossState, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, kind, amount, payload FROM events WHERE account_id = ? AND kind IN (?, ?, ?) ORDER BY seq ASC`,
		accountID, kindLoss, kindReward, kindCompensation)
	if err != nil {
		return lossState{}, fmt.Errorf("ledger: read recovery history for %s: %w", accountID, err)
	}
	defer rows.Close()

	queue := make([]*recoveryEntry, 0, 8)
	var burned int64
	for rows.Next() {
		var id, kind, payload string
		var amount int64
		if err := rows.Scan(&id, &kind, &amount, &payload); err != nil {
			return lossState{}, fmt.Errorf("ledger: scan recovery history: %w", err)
		}
		switch kind {
		case kindLoss:
			burn := maxInt64(-amount, 0)
			burned += burn
			queue = append(queue, &recoveryEntry{id: id, original: burn, remaining: burn})
		case kindReward:
			var decoded rewardPayload
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				return lossState{}, errw(ErrPayloadConflict, fmt.Errorf("reward %s payload: %w", id, err))
			}
			repayOldest(queue, decoded.AppliedToLoss)
		case kindCompensation:
			var decoded compensationPayload
			if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
				return lossState{}, errw(ErrPayloadConflict, fmt.Errorf("compensation %s payload: %w", id, err))
			}
			for _, entry := range queue {
				if entry.id == decoded.OriginalEventID {
					entry.remaining = maxInt64(entry.remaining-decoded.Amount, 0)
					break
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return lossState{}, fmt.Errorf("ledger: read recovery history for %s: %w", accountID, err)
	}

	state := lossState{Remaining: make(map[string]int64, len(queue))}
	for _, entry := range queue {
		state.Outstanding += entry.remaining
		state.Remaining[entry.id] = entry.remaining
		if state.Target == "" && entry.remaining > 0 {
			state.Target = entry.id
			state.TargetBurned = entry.original
		}
	}
	state.Burned = burned
	state.Recovered = burned - state.Outstanding
	return state, nil
}

func repayOldest(queue []*recoveryEntry, amount int64) {
	for _, entry := range queue {
		if amount <= 0 {
			return
		}
		if entry.remaining <= 0 {
			continue
		}
		take := minInt64(amount, entry.remaining)
		entry.remaining -= take
		amount -= take
	}
}

// Lesson is one canonical behavioral rule.
type Lesson struct {
	ID            int64  `json:"id"`
	EventID       string `json:"event_id"`
	AccountID     string `json:"account_id"`
	Rule          string `json:"rule"`
	Correction    string `json:"correction"`
	Scope         string `json:"scope"`
	Exceptions    string `json:"exceptions"`
	Evidence      string `json:"evidence"`
	PolicyVersion string `json:"policy_version"`
	At            string `json:"at"`
}

// Lessons reads the canonical lessons of an identity, oldest first.
func (l *Ledger) Lessons(ctx context.Context, accountID string) ([]Lesson, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT id, event_id, account_id, rule, correction, scope, exceptions, evidence, policy_version, at
		FROM lessons WHERE account_id = ? ORDER BY id ASC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("ledger: read lessons for %s: %w", accountID, err)
	}
	defer rows.Close()
	out := make([]Lesson, 0, 8)
	for rows.Next() {
		var lesson Lesson
		if err := rows.Scan(&lesson.ID, &lesson.EventID, &lesson.AccountID, &lesson.Rule,
			&lesson.Correction, &lesson.Scope, &lesson.Exceptions, &lesson.Evidence,
			&lesson.PolicyVersion, &lesson.At); err != nil {
			return nil, fmt.Errorf("ledger: scan lesson: %w", err)
		}
		out = append(out, lesson)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read lessons for %s: %w", accountID, err)
	}
	return out, nil
}
