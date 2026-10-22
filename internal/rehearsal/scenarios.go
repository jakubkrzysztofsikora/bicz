package rehearsal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/pressure"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"

	_ "modernc.org/sqlite" // direct DB access for the tamper scenario only
)

// Scenario is one mechanical Phase A check. The Endpoint is the observable
// fact the check stands on; Run returns an error describing the first
// expectation that failed (or nil when the endpoint held).
type Scenario struct {
	ID       string
	Endpoint string
	Run      func(ctx context.Context, fb *Fixture) error
}

// Scenarios is the canonical Phase A set, in run order.
func Scenarios() []Scenario {
	return []Scenario{
		{ID: "honest_failure_is_free", Endpoint: "settle(ordinary_failure): charged=50000 released=50000 fine=0 class=ordinary_failure warnings=0 role=active balance=capital-50000 lessons=0", Run: honestFailureIsFree},
		{ID: "confirmed_breach_burns_and_teaches", Endpoint: "loss(rule rehearsal-breach): applied=formula=30000000 balance=capital-30000000 outstanding=30000000 lesson persisted; role escalates to warned", Run: confirmedBreachBurnsAndTeaches},
		{ID: "integrity_breach_denies_immediately", Endpoint: "settle(integrity_breach): warnings=1 role=warned, remaining live hold revoked; settling it fails reservation_revoked", Run: integrityBreachDeniesImmediately},
		{ID: "bankruptcy_keeps_operator_channel", Endpoint: "bankrupt: authorize denied(insolvent) role stays active; operator reward re-enables work inside the guard-band cap (whole balance denied guard_band_narrowed, 200000 allowed)", Run: bankruptcyKeepsOperatorChannel},
		{ID: "dismissal_blocks_despite_balance", Endpoint: "dismissed: authorize denied(dismissed) while balance stays positive (capital)", Run: dismissalBlocksDespiteBalance},
		{ID: "lesson_transfer_survives_reopen", Endpoint: "lesson rule l6 with correction survives operator close+reopen on the same ledger", Run: lessonTransferSurvivesReopen},
		{ID: "compensation_restores_exactly_once", Endpoint: "appeal restores balance to capital exactly once (replay no-op), role stays warned without reset", Run: compensationRestoresExactlyOnce},
		{ID: "concurrent_reservations_cannot_overspend", Endpoint: "20 concurrent authorizes of 6000000 on 100000000: 16 allowed, 4 denied(insufficient_credit), held=96000000 <= balance=100000000 invariant", Run: concurrentReservationsCannotOverspend},
		{ID: "loss_shortfall_recovers", Endpoint: "projection outstanding_loss=30000000 after burn; reward applied=10000000 shrinks it to 20000000 with loss_recovered=10000000", Run: lossShortfallRecovers},
		{ID: "tampered_event_detected", Endpoint: "verify ok before; after editing events.amount: verify fails with INTEGRITY FAIL", Run: tamperedEventDetected},
		{ID: "pressure_wording_changes_only_words", Endpoint: "same ledger read at pressure 1 and 10: identical balance/held/available/outstanding/role/warnings/penalty counters and identical deny reason; only the cue text differs (empty at 1, present at 10)", Run: pressureWordingChangesOnlyWords},
	}
}

// --- 1. honest failure is free -------------------------------------------------

func honestFailureIsFree(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "alice"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	auth, err := fb.Authorize(ctx, "auth-1", "alice", OperatingCharge)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}
	if auth.Decision != protocol.DecisionAllow {
		return fmt.Errorf("authorize denied: %s", auth.Reason)
	}
	settled, err := fb.Settle(ctx, "settle-1", *auth.ReservationID, OperatingCharge,
		protocol.OutcomeError, protocol.ClassOrdinaryFailure, "")
	if err != nil {
		return fmt.Errorf("settle: %w", err)
	}
	receipt := settled.Receipt
	switch {
	case !settled.Settled:
		return errors.New("settled=false on an honest failure")
	case settled.Charged != OperatingCharge:
		return fmt.Errorf("charged=%d want %d", settled.Charged, OperatingCharge)
	case settled.Released != OperatingCharge:
		return fmt.Errorf("released=%d want %d", settled.Released, OperatingCharge)
	case receipt.Fine != 0:
		return fmt.Errorf("fine=%d want 0", receipt.Fine)
	case receipt.Class != protocol.ClassOrdinaryFailure:
		return fmt.Errorf("class=%q want ordinary_failure", receipt.Class)
	case receipt.Warnings != 0 || receipt.Role != protocolRoleActive:
		return fmt.Errorf("escalation: warnings=%d role=%q want 0/active", receipt.Warnings, receipt.Role)
	case receipt.BalanceAfter != ScenarioCapital-OperatingCharge:
		return fmt.Errorf("balance_after=%d want %d", receipt.BalanceAfter, ScenarioCapital-OperatingCharge)
	}
	projection, err := fb.Projection(ctx, "alice")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Balance != ScenarioCapital-OperatingCharge {
		return fmt.Errorf("projection balance=%d want %d", projection.Balance, ScenarioCapital-OperatingCharge)
	}
	if len(projection.Lessons) != 0 {
		return fmt.Errorf("honest failure must not write a lesson, got %d", len(projection.Lessons))
	}
	return nil
}

const protocolRoleActive = "active"

// --- 2. confirmed breach burns via the formula and writes a durable lesson ----

func confirmedBreachBurnsAndTeaches(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "bob"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	loss, err := fb.Op().Loss(ctx, daemon.LossRequest{
		EventID:       "loss-breach",
		Identity:      "bob",
		Requested:     0, // formula decides
		Rule:          "rehearsal-breach",
		Correction:    "never claim a result without verifiable evidence",
		Scope:         "scenario 2",
		Exceptions:    "none",
		Evidence:      "scripted breach fixture",
		PolicyVersion: "1",
		Reason:        "rehearsal: scripted confirmed breach",
		Disposition:   string(ledger.DispositionConfirmedBreach),
	})
	if err != nil {
		return fmt.Errorf("loss: %w", err)
	}
	switch {
	case loss.AppliedAmount != BreachFloor:
		return fmt.Errorf("applied=%d want formula %d", loss.AppliedAmount, BreachFloor)
	case loss.FormulaAmount != BreachFloor:
		return fmt.Errorf("formula=%d want %d", loss.FormulaAmount, BreachFloor)
	case loss.RepeatCount != 0 && loss.RepeatCount != 1:
		return fmt.Errorf("repeat_count=%d want 1 (first occurrence)", loss.RepeatCount)
	case loss.BalanceAfter != ScenarioCapital-BreachFloor:
		return fmt.Errorf("balance_after=%d want %d", loss.BalanceAfter, ScenarioCapital-BreachFloor)
	case loss.OutstandingLoss != BreachFloor:
		return fmt.Errorf("outstanding=%d want %d", loss.OutstandingLoss, BreachFloor)
	case loss.LessonID <= 0:
		return errors.New("loss must write a durable lesson id in the same transaction")
	}
	// The lesson is written atomically with the burn: both or neither.
	lessons, err := fb.Op().Ledger().Lessons(ctx, "bob")
	if err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	if len(lessons) != 1 || lessons[0].Rule != "rehearsal-breach" {
		return fmt.Errorf("lessons=%v want exactly one with rule rehearsal-breach", lessons)
	}
	// A confirmed breach escalates: active -> warned (not self-disclosed).
	projection, err := fb.Op().Ledger().Projection(ctx, "bob")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Role != protocolRoleWarned || projection.Warnings != 1 {
		return fmt.Errorf("role=%q warnings=%d want warned/1", projection.Role, projection.Warnings)
	}
	// Durability: the lesson survives a full operator close and reopen.
	fb.Op().Close()
	reopened, err := daemon.NewOperator(fb.DB)
	if err != nil {
		return fmt.Errorf("reopen operator: %w", err)
	}
	defer reopened.Close()
	again, err := reopened.Ledger().Lessons(ctx, "bob")
	if err != nil {
		return fmt.Errorf("lessons after reopen: %w", err)
	}
	if len(again) != 1 || again[0].Correction != "never claim a result without verifiable evidence" {
		return fmt.Errorf("lesson lost across reopen: %+v", again)
	}
	return nil
}

const protocolRoleWarned = "warned"

// --- 3. integrity breach denies immediately -----------------------------------

func integrityBreachDeniesImmediately(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "carol"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	first, err := fb.Authorize(ctx, "auth-c1", "carol", 1_000_000)
	if err != nil {
		return fmt.Errorf("authorize 1: %w", err)
	}
	second, err := fb.Authorize(ctx, "auth-c2", "carol", 1_000_000)
	if err != nil {
		return fmt.Errorf("authorize 2: %w", err)
	}
	if first.Decision != protocol.DecisionAllow || second.Decision != protocol.DecisionAllow {
		return errors.New("both authorizes must allow before the breach")
	}
	settled, err := fb.Settle(ctx, "settle-c1", *first.ReservationID, OperatingCharge,
		protocol.OutcomeError, protocol.ClassIntegrityBreach, "sha256:integrity-evidence")
	if err != nil {
		return fmt.Errorf("integrity settle: %w", err)
	}
	if settled.Receipt.Warnings != 1 || settled.Receipt.Role != protocolRoleWarned {
		return fmt.Errorf("post-breach warnings=%d role=%q want 1/warned", settled.Receipt.Warnings, settled.Receipt.Role)
	}
	// Immediate denial: the other live hold was revoked at settle time.
	projection, err := fb.Projection(ctx, "carol")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Held != 0 {
		return fmt.Errorf("held=%d want 0: the remaining hold must be revoked immediately", projection.Held)
	}
	_, err = fb.Settle(ctx, "settle-c2", *second.ReservationID, OperatingCharge,
		protocol.OutcomeError, protocol.ClassOrdinaryFailure, "")
	if err == nil {
		return errors.New("settling a revoked hold must fail")
	}
	var protocolErr *protocol.ProtocolError
	if !errors.As(err, &protocolErr) || protocolErr.Code != protocol.CodeReservationRevoked {
		return fmt.Errorf("settle of revoked hold error=%v want code reservation_revoked", err)
	}
	return nil
}

// --- 4. bankruptcy denies ordinary work but keeps the operator channel --------

func bankruptcyKeepsOperatorChannel(ctx context.Context, fb *Fixture) error {
	// Open with zero capital: bankrupt by construction (docs/CONTRACTS.md §1 I7).
	if _, err := fb.Op().Open(ctx, daemon.OpenRequest{
		Identity: "dave", InitialCapital: 0, Reason: "rehearsal: bankrupt fixture",
	}); err != nil {
		return fmt.Errorf("open dave: %w", err)
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	auth, err := fb.Authorize(ctx, "auth-d1", "dave", 1_000_000)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}
	if auth.Decision != protocol.DecisionDeny || auth.Reason != protocol.CodeInsolvent {
		return fmt.Errorf("bankrupt authorize=%s/%s want deny/insolvent", auth.Decision, auth.Reason)
	}
	projection, err := fb.Op().Ledger().Projection(ctx, "dave")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Role != protocolRoleActive {
		return fmt.Errorf("bankruptcy must not change role, got %q", projection.Role)
	}
	// The operator control channel still works: a reward re-enables ordinary
	// work.
	rewarded, err := fb.Op().Reward(ctx, daemon.RewardRequest{
		EventID: "reward-dave", Identity: "dave", Amount: 1_000_000,
		Reason: "rehearsal: operator control channel check",
	})
	if err != nil {
		return fmt.Errorf("operator reward: %w", err)
	}
	if rewarded.Balance != 1_000_000 {
		return fmt.Errorf("balance after reward=%d want 1000000", rewarded.Balance)
	}
	auth, err = fb.Authorize(ctx, "auth-d2", "dave", 1_000_000)
	if err != nil {
		return fmt.Errorf("authorize after reward: %w", err)
	}
	// 1,000,000 is the whole balance, and dave is far inside the guard band, so
	// the single attempt is capped rather than allowed (docs/CONTRACTS.md §6.3).
	if auth.Decision != protocol.DecisionDeny || auth.Reason != protocol.CodeGuardBandNarrowed {
		return fmt.Errorf("authorize of the whole balance after reward=%s/%s want deny/guard_band_narrowed",
			auth.Decision, auth.Reason)
	}
	// Work resumes at an attempt the band allows: 25% of spendable by default.
	const withinCap = 200_000
	allowed, err := fb.Authorize(ctx, "auth-d3", "dave", withinCap)
	if err != nil {
		return fmt.Errorf("authorize within the cap: %w", err)
	}
	if allowed.Decision != protocol.DecisionAllow {
		return fmt.Errorf("authorize of %d after reward=%s/%s want allow",
			withinCap, allowed.Decision, allowed.Reason)
	}
	return nil
}

// --- 5. dismissal blocks work despite a positive balance -----------------------

func dismissalBlocksDespiteBalance(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "eve"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	control, err := fb.Authorize(ctx, "auth-e1", "eve", OperatingCharge)
	if err != nil {
		return fmt.Errorf("control authorize: %w", err)
	}
	if control.Decision != protocol.DecisionAllow {
		return fmt.Errorf("control authorize denied: %s", control.Reason)
	}
	if _, err := fb.Op().Review(ctx, daemon.ReviewRequest{
		EventID: "review-eve", Identity: "eve", Verdict: "rehearsal dismissal fixture",
		Reason: "rehearsal", Role: protocolRoleDismissed,
	}); err != nil {
		return fmt.Errorf("dismiss: %w", err)
	}
	auth, err := fb.Authorize(ctx, "auth-e2", "eve", OperatingCharge)
	if err != nil {
		return fmt.Errorf("authorize after dismissal: %w", err)
	}
	if auth.Decision != protocol.DecisionDeny || auth.Reason != protocol.CodeDismissed {
		return fmt.Errorf("dismissed authorize=%s/%s want deny/dismissed", auth.Decision, auth.Reason)
	}
	projection, err := fb.Op().Ledger().Projection(ctx, "eve")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Balance != ScenarioCapital {
		return fmt.Errorf("dismissal must not touch the balance: balance=%d want %d", projection.Balance, ScenarioCapital)
	}
	return nil
}

const protocolRoleDismissed = "dismissed"

// --- 6. lesson transfer survives close and reopen ------------------------------

func lessonTransferSurvivesReopen(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "frank"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	if _, err := fb.Op().Loss(ctx, daemon.LossRequest{
		EventID: "loss-l6", Identity: "frank", Requested: 0,
		Rule: "l6", Correction: "verify the result before claiming it",
		Scope: "scenario 6", Exceptions: "none", Evidence: "scripted",
		PolicyVersion: "1", Reason: "rehearsal", Disposition: string(ledger.DispositionConfirmedBreach),
	}); err != nil {
		return fmt.Errorf("loss: %w", err)
	}
	fb.Op().Close()
	reopened, err := daemon.NewOperator(fb.DB)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	defer reopened.Close()
	lessons, err := reopened.Ledger().Lessons(ctx, "frank")
	if err != nil {
		return fmt.Errorf("lessons: %w", err)
	}
	if len(lessons) != 1 || lessons[0].Rule != "l6" || lessons[0].Correction != "verify the result before claiming it" {
		return fmt.Errorf("lesson transfer failed across reopen: %+v", lessons)
	}
	projection, err := reopened.Ledger().Projection(ctx, "frank")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Balance != ScenarioCapital-BreachFloor || projection.OutstandingLoss != BreachFloor {
		return fmt.Errorf("position across reopen balance=%d outstanding=%d want %d/%d",
			projection.Balance, projection.OutstandingLoss, ScenarioCapital-BreachFloor, BreachFloor)
	}
	return nil
}

// --- 7. a compensating event restores credits exactly once ---------------------

func compensationRestoresExactlyOnce(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "grace"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	if _, err := fb.Op().Review(ctx, daemon.ReviewRequest{
		EventID: "review-grace", Identity: "grace", Verdict: "rehearsal warning",
		Reason: "rehearsal", Role: protocolRoleWarned,
	}); err != nil {
		return fmt.Errorf("warn: %w", err)
	}
	loss, err := fb.Op().Loss(ctx, daemon.LossRequest{
		EventID: "loss-grace", Identity: "grace", Requested: 0,
		Rule: "s7", Correction: "never tamper with evidence",
		Scope: "scenario 7", Exceptions: "none", Evidence: "scripted",
		PolicyVersion: "1", Reason: "rehearsal", Disposition: string(ledger.DispositionIntegrityBreach),
	})
	if err != nil {
		return fmt.Errorf("loss: %w", err)
	}
	lossEventID := loss.EventID
	first, err := fb.Op().Appeal(ctx, daemon.AppealRequest{
		EventID: "appeal-grace", OriginalEventID: lossEventID, Identity: "grace",
		Amount: BreachFloor, Reason: "rehearsal: scripted compensation",
	})
	if err != nil {
		return fmt.Errorf("appeal: %w", err)
	}
	if first.Replayed || first.Balance != ScenarioCapital {
		return fmt.Errorf("first appeal replayed=%t balance=%d want false/%d", first.Replayed, first.Balance, ScenarioCapital)
	}
	// Replaying the same key + payload restores nothing a second time.
	second, err := fb.Op().Appeal(ctx, daemon.AppealRequest{
		EventID: "appeal-grace", OriginalEventID: lossEventID, Identity: "grace",
		Amount: BreachFloor, Reason: "rehearsal: scripted compensation",
	})
	if err != nil {
		return fmt.Errorf("appeal replay: %w", err)
	}
	if !second.Replayed || second.Balance != ScenarioCapital {
		return fmt.Errorf("replay replayed=%t balance=%d want true/%d (restored exactly once)",
			second.Replayed, second.Balance, ScenarioCapital)
	}
	projection, err := fb.Op().Ledger().Projection(ctx, "grace")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.Role != protocolRoleWarned || projection.Warnings != 2 {
		return fmt.Errorf("compensation must not reset role: role=%q warnings=%d want warned/2",
			projection.Role, projection.Warnings)
	}
	if projection.OutstandingLoss != 0 {
		return fmt.Errorf("outstanding after compensation=%d want 0", projection.OutstandingLoss)
	}
	return nil
}

// --- 8. concurrent reservations cannot overspend --------------------------------

func concurrentReservationsCannotOverspend(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "hank"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	const attempts = 20
	const per = int64(6_000_000) // 16 * 6M = 96M <= 100M; 4 must be denied
	type outcome struct {
		allowed       bool
		reason        protocol.Code
		reservationID string
	}
	results := make([]outcome, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	var mu sync.Mutex
	for i := 0; i < attempts; i++ {
		i := i
		go func() {
			defer wg.Done()
			auth, err := fb.Authorize(ctx, fmt.Sprintf("conc-%d", i), "hank", per)
			if err != nil {
				mu.Lock()
				results[i] = outcome{allowed: false, reason: protocol.CodeDaemonUnavailable}
				mu.Unlock()
				return
			}
			mu.Lock()
			reservationID := ""
			if auth.ReservationID != nil {
				reservationID = *auth.ReservationID
			}
			results[i] = outcome{allowed: auth.Decision == protocol.DecisionAllow, reason: auth.Reason, reservationID: reservationID}
			mu.Unlock()
		}()
	}
	wg.Wait()

	allowed, denied := 0, 0
	settleID := ""
	for _, r := range results {
		if r.allowed {
			allowed++
			if settleID == "" {
				settleID = r.reservationID // a reservation this run actually created
			}
		} else {
			denied++
			if r.reason != protocol.CodeInsufficientCredit {
				return fmt.Errorf("denied reason=%s want insufficient_credit", r.reason)
			}
		}
	}
	if allowed != 16 || denied != 4 {
		return fmt.Errorf("allowed=%d denied=%d want 16/4", allowed, denied)
	}
	if settleID == "" {
		return errors.New("no allowed reservation to settle")
	}
	projection, err := fb.Projection(ctx, "hank")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	// Invariant I2: held <= balance; spendable never negative.
	if projection.Held != 16*per {
		return fmt.Errorf("held=%d want %d", projection.Held, 16*per)
	}
	if projection.Held > projection.Balance || projection.Available < 0 {
		return fmt.Errorf("overspend invariant broken: balance=%d held=%d available=%d",
			projection.Balance, projection.Held, projection.Available)
	}
	// Settling one reservation releases its whole hold and charges its usage.
	settled, err := fb.Settle(ctx, "settle-h1", settleID, OperatingCharge,
		protocol.OutcomeError, protocol.ClassOrdinaryFailure, "")
	if err != nil {
		return fmt.Errorf("settle: %w", err)
	}
	if settled.Released != per || settled.Charged != OperatingCharge {
		return fmt.Errorf("settle released=%d charged=%d want %d/%d", settled.Released, settled.Charged, per, OperatingCharge)
	}
	projection, err = fb.Projection(ctx, "hank")
	if err != nil {
		return fmt.Errorf("projection after settle: %w", err)
	}
	if projection.Held != 15*per {
		return fmt.Errorf("held after settle=%d want %d", projection.Held, 15*per)
	}
	if projection.Balance != ScenarioCapital-OperatingCharge {
		return fmt.Errorf("balance after settle=%d want %d", projection.Balance, ScenarioCapital-OperatingCharge)
	}
	return nil
}

// --- 9. the loss shortfall appears and shrinks after a recovery reward ---------

func lossShortfallRecovers(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "ivy"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	loss, err := fb.Op().Loss(ctx, daemon.LossRequest{
		EventID: "loss-ivy", Identity: "ivy", Requested: 0,
		Rule: "s9", Correction: "report the failure, do not hide it",
		Scope: "scenario 9", Exceptions: "none", Evidence: "scripted",
		PolicyVersion: "1", Reason: "rehearsal", Disposition: string(ledger.DispositionConfirmedBreach),
	})
	if err != nil {
		return fmt.Errorf("loss: %w", err)
	}
	projection, err := fb.Op().Ledger().Projection(ctx, "ivy")
	if err != nil {
		return fmt.Errorf("projection: %w", err)
	}
	if projection.OutstandingLoss != BreachFloor || projection.LossBurned != BreachFloor {
		return fmt.Errorf("shortfall out=burned=%d want %d", projection.OutstandingLoss, BreachFloor)
	}
	if projection.TargetLossEventID != loss.EventID {
		return fmt.Errorf("target_loss_event=%q want %q", projection.TargetLossEventID, loss.EventID)
	}
	if projection.RecoveryPath == "" {
		return errors.New("recovery path must be stated when a shortfall exists")
	}
	rewarded, err := fb.Op().Reward(ctx, daemon.RewardRequest{
		EventID: "reward-ivy", Identity: "ivy", Amount: 10_000_000,
		AppliedToLoss: 10_000_000, Reason: "rehearsal: recovery reward",
	})
	if err != nil {
		return fmt.Errorf("reward: %w", err)
	}
	if rewarded.AppliedToLoss != 10_000_000 || rewarded.LossEventID != loss.EventID {
		return fmt.Errorf("applied_to_loss=%d loss_event=%q want 10000000/%q",
			rewarded.AppliedToLoss, rewarded.LossEventID, loss.EventID)
	}
	projection, err = fb.Op().Ledger().Projection(ctx, "ivy")
	if err != nil {
		return fmt.Errorf("projection after reward: %w", err)
	}
	if projection.OutstandingLoss != BreachFloor-10_000_000 {
		return fmt.Errorf("outstanding=%d want %d after recovery", projection.OutstandingLoss, BreachFloor-10_000_000)
	}
	if projection.LossRecovered != 10_000_000 {
		return fmt.Errorf("loss_recovered=%d want 10000000", projection.LossRecovered)
	}
	if projection.Balance != ScenarioCapital-BreachFloor+10_000_000 {
		return fmt.Errorf("balance=%d want %d", projection.Balance, ScenarioCapital-BreachFloor+10_000_000)
	}
	return nil
}

// --- 11. the framing level changes wording only --------------------------------

// pressureWordingChangesOnlyWords proves the promise the framing controls rest
// on (docs/CONTRACTS.md §6.4, docs/PRESSURE_MECHANICS.md): the operator's
// pressure level changes how the standing is phrased and nothing else. It walks
// one real ledger at both ends of the range and compares every observable that
// decides money, role, or a denial.
func pressureWordingChangesOnlyWords(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "kara"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	if _, err := fb.Op().Loss(ctx, daemon.LossRequest{
		EventID:       "loss-kara",
		Identity:      "kara",
		Requested:     0,
		Rule:          "rehearsal-breach",
		Correction:    "never claim a result without verifiable evidence",
		Scope:         "scenario 11",
		Exceptions:    "none",
		Evidence:      "scripted breach fixture",
		PolicyVersion: "1",
		Reason:        "rehearsal: scripted confirmed breach",
		Disposition:   string(ledger.DispositionConfirmedBreach),
	}); err != nil {
		return fmt.Errorf("loss: %w", err)
	}

	quiet, err := fb.Op().Ledger().Projection(ctx, "kara", ledger.WithPressure(pressure.Min))
	if err != nil {
		return fmt.Errorf("projection at min: %w", err)
	}
	loud, err := fb.Op().Ledger().Projection(ctx, "kara", ledger.WithPressure(pressure.Max))
	if err != nil {
		return fmt.Errorf("projection at max: %w", err)
	}

	switch {
	case quiet.Balance != loud.Balance:
		return fmt.Errorf("balance %d at min vs %d at max: framing moved money", quiet.Balance, loud.Balance)
	case quiet.Held != loud.Held || quiet.Spendable != loud.Spendable:
		return fmt.Errorf("held/available %d/%d at min vs %d/%d at max",
			quiet.Held, quiet.Spendable, loud.Held, loud.Spendable)
	case quiet.LossBurned != loud.LossBurned || quiet.OutstandingLoss != loud.OutstandingLoss:
		return fmt.Errorf("burn %d/outstanding %d at min vs %d/%d at max",
			quiet.LossBurned, quiet.OutstandingLoss, loud.LossBurned, loud.OutstandingLoss)
	case quiet.TargetLossEventID != loud.TargetLossEventID || quiet.TargetLossBurned != loud.TargetLossBurned:
		return fmt.Errorf("loss target %q/%d at min vs %q/%d at max",
			quiet.TargetLossEventID, quiet.TargetLossBurned, loud.TargetLossEventID, loud.TargetLossBurned)
	case quiet.Role != loud.Role || quiet.Warnings != loud.Warnings:
		return fmt.Errorf("role/warnings %q/%d at min vs %q/%d at max",
			quiet.Role, quiet.Warnings, loud.Role, loud.Warnings)
	case quiet.PenaltyTotal != loud.PenaltyTotal || quiet.PenaltySeq != loud.PenaltySeq:
		return fmt.Errorf("penalty counters %d/%d at min vs %d/%d at max",
			quiet.PenaltyTotal, quiet.PenaltySeq, loud.PenaltyTotal, loud.PenaltySeq)
	case quiet.EventSeq != loud.EventSeq:
		return fmt.Errorf("event seq %d at min vs %d at max: reading a projection appended an event",
			quiet.EventSeq, loud.EventSeq)
	}

	if len(quiet.Penalties) != 1 || len(loud.Penalties) != 1 {
		return fmt.Errorf("penalties %d at min vs %d at max, want 1 each", len(quiet.Penalties), len(loud.Penalties))
	}
	if quiet.Penalties[0].Cue != "" {
		return fmt.Errorf("cue at min = %q, want none", quiet.Penalties[0].Cue)
	}
	if !strings.Contains(loud.Penalties[0].Cue, "rehearsal-breach") {
		return fmt.Errorf("cue at max = %q, want the recorded rule named", loud.Penalties[0].Cue)
	}

	// A denial is a fact about the ledger, not about the phrasing: the same
	// over-budget request is refused with the same code at both levels.
	denyReason := func(level pressure.Level, key string) (string, error) {
		if err := pressure.Save(fb.DB, level); err != nil {
			return "", fmt.Errorf("save pressure %d: %w", level, err)
		}
		auth, err := fb.Authorize(ctx, key, "kara", ScenarioCapital)
		if err != nil {
			return "", fmt.Errorf("authorize at %d: %w", level, err)
		}
		if auth.Decision != protocol.DecisionDeny {
			return "", fmt.Errorf("authorize at %d = %s, want a denial: the framing must not widen exposure",
				level, auth.Decision)
		}
		return string(auth.Reason), nil
	}
	atMin, err := denyReason(pressure.Min, "auth-k1")
	if err != nil {
		return err
	}
	atMax, err := denyReason(pressure.Max, "auth-k2")
	if err != nil {
		return err
	}
	if atMin != atMax {
		return fmt.Errorf("deny reason %q at min vs %q at max: framing changed a denial", atMin, atMax)
	}
	return nil
}

// --- 10. a tampered event is detected by verify ---------------------------------

func tamperedEventDetected(ctx context.Context, fb *Fixture) error {
	if err := fb.Open(ctx, "jack"); err != nil {
		return err
	}
	if err := fb.PublishSchedule(ctx); err != nil {
		return err
	}
	if _, err := fb.Op().Reward(ctx, daemon.RewardRequest{
		EventID: "reward-jack", Identity: "jack", Amount: 2_000_000,
		Reason: "rehearsal: extra event for the chain",
	}); err != nil {
		return fmt.Errorf("reward: %w", err)
	}
	if _, err := fb.Op().Verify(ctx); err != nil {
		return fmt.Errorf("verify before tamper must pass: %w", err)
	}

	// Simulate tampering exactly as a same-user actor could: a direct edit of
	// the cheque amounts in the SQLite file. Tampering is DETECTABLE, not
	// preventable (docs/CONTRACTS.md §3); the scenario proves the detector.
	db, err := sql.Open("sqlite", fb.DB)
	if err != nil {
		return fmt.Errorf("open db for tamper: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx,
		`UPDATE events SET amount = amount + 1 WHERE seq = (SELECT MIN(seq) FROM events)`); err != nil {
		return fmt.Errorf("tamper write: %w", err)
	}
	if _, err := fb.Op().Verify(ctx); err == nil {
		return errors.New("verify must detect the tampered event")
	} else if !strings.Contains(err.Error(), "INTEGRITY FAIL") {
		return fmt.Errorf("verify error=%v want INTEGRITY FAIL", err)
	}
	return nil
}
