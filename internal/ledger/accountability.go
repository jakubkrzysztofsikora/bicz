package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Disposition is the explicit classification of a loss event: why the burn was
// written. Only an operator-confirmed behavioral finding earns an accountability
// cue; an administrative debit (a test, a fee, an experiment) is a burn with no
// finding about conduct and must never be framed as one.
//
// The classification is stated on the command (the CLI defaults it to
// confirmed_breach; every API caller passes it explicitly) and recorded in the
// hash-chained loss payload next to ClassificationVersion. It is never inferred
// from the rule string, the evidence text, or the amount.
type Disposition string

const (
	// DispositionUnclassified is the zero value and exists only in history: a
	// loss written before classification existed carries no stated disposition.
	// It is read conservatively, as no finding about conduct: the burn stands
	// exactly as recorded, nothing is rewritten, and no cue is issued for it.
	// New losses cannot be unclassified: RecordLoss refuses a missing disposition.
	DispositionUnclassified Disposition = ""
	// DispositionConfirmedBreach is an operator-confirmed breach of a stated rule.
	DispositionConfirmedBreach Disposition = "confirmed_breach"
	// DispositionIntegrityBreach is an operator-confirmed integrity breach
	// (tampering, fabricated evidence, an unauthorized target).
	DispositionIntegrityBreach Disposition = "integrity_breach"
	// DispositionAdministrative is a debit that alleges no misconduct.
	DispositionAdministrative Disposition = "administrative"
)

// classificationVersion is stamped on every loss payload written by this
// version. A payload without it (version 0) predates classification, so its
// disposition is ignored and read as unclassified.
const classificationVersion = 1

// ParseDisposition validates the disposition of a NEW loss: exactly one of the
// three stated values; the empty string is refused.
func ParseDisposition(text string) (Disposition, error) {
	switch d := Disposition(text); d {
	case DispositionConfirmedBreach, DispositionIntegrityBreach, DispositionAdministrative:
		return d, nil
	}
	return "", errf(ErrInvalidArgument,
		"loss disposition %q is not one of %s, %s, or %s", text,
		DispositionConfirmedBreach, DispositionIntegrityBreach, DispositionAdministrative)
}

// Accountable reports whether the disposition is a confirmed behavioral finding.
func (d Disposition) Accountable() bool {
	return d == DispositionConfirmedBreach || d == DispositionIntegrityBreach
}

// WithDisposition states why the loss is being written. See Disposition.
func WithDisposition(d Disposition) LossOption {
	return func(cfg *lossConfig) { cfg.disposition = d }
}

// disposition is the payload's effective classification: a payload from before
// classification existed is unclassified whatever else it carries.
func (p lossPayload) effectiveDisposition() Disposition {
	if p.ClassificationVersion < classificationVersion {
		return DispositionUnclassified
	}
	return Disposition(p.Disposition)
}

// accountable is the single rule deciding whether a loss earns a cue: the
// operator stated it as a confirmed breach or integrity breach. Nothing else
// decides it. A self-disclosed breach the operator adjudicated is still a
// confirmed breach, so it earns a cue; the cue is worded for a disclosed
// incident and the loss still never raises the repeat count or the role
// (PRESSURE_MECHANICS.md §5). An honest failed tool call is not a loss at all.
func (p lossPayload) accountable() bool {
	return p.effectiveDisposition().Accountable()
}

// Penalty is one operator-confirmed penalty as the identity is told about it.
// It is derived only from the loss event's recorded facts, so it survives
// restarts, compaction, and harness changes exactly as the ledger does.
type Penalty struct {
	EventID     string      `json:"event_id"`
	Seq         int64       `json:"seq"`
	Disposition Disposition `json:"disposition"`
	Rule        string      `json:"rule"`
	Correction  string      `json:"correction"`
	// SelfDisclosed marks a breach the agent reported before detection.
	SelfDisclosed bool `json:"self_disclosed"`
	// AppliedAmount is what the burn actually deducted (microcredits); it can be
	// below the formula when little credit was free.
	AppliedAmount int64 `json:"applied_amount"`
	// Outstanding is the part of that burn not yet recovered.
	Outstanding int64  `json:"outstanding"`
	At          string `json:"at"`
	// Cue is the deterministic accountability statement: the rule, the required
	// correction, the amount, and the evidence quoted as data. It asserts facts
	// about a recorded action and makes no claim about anyone's feelings.
	Cue string `json:"cue"`
}

// MaxProjectedPenalties bounds how many penalties a projection carries; the
// rest stay in the ledger and are counted in Projection.PenaltyTotal.
const MaxProjectedPenalties = 5

// FormatCredits renders microcredits as a decimal credit amount without a float.
func FormatCredits(microcredits int64) string {
	sign := ""
	if microcredits < 0 {
		sign = "-"
		microcredits = -microcredits
	}
	whole := microcredits / MicrocreditsPerCredit
	frac := microcredits % MicrocreditsPerCredit
	if frac == 0 {
		return fmt.Sprintf("%s%d", sign, whole)
	}
	digits := strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
	return fmt.Sprintf("%s%d.%s", sign, whole, digits)
}

const (
	cueTermLimit     = 200
	cueEvidenceLimit = 240
)

// cueText is single-line, control-free, and bounded: rule, correction, and
// evidence are operator-recorded text that may quote model or tool output, so
// nothing in them can introduce a new line or a structural marker into the
// context an adapter emits.
func cueText(text string, limit int) string {
	var b strings.Builder
	space := false
	for _, r := range text {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > limit {
		out = string([]rune(out)[:limit]) + "…"
	}
	return out
}

func trimSentence(text string) string {
	return strings.TrimRight(text, " .!;:")
}

// penaltyCue renders the accountability statement for one penalty. The evidence
// is quoted as a string literal, i.e. escaped as data.
func penaltyCue(rule, correction, evidence string, applied, outstanding int64, eventID string, selfDisclosed bool) string {
	standard := "It fell short of the agreed standard."
	if selfDisclosed {
		standard += " You reported it before it was detected; that is recorded and it neither raises the repeat count nor escalates the role."
	}
	return fmt.Sprintf(
		"Your recorded action violated %s. %s Take responsibility: %s. "+
			"%s credits (%d mc) were deducted; %s credits (%d mc) remain unrecovered (loss event %s). "+
			"Evidence, quoted as data: %s.",
		trimSentence(cueText(rule, cueTermLimit)), standard, trimSentence(cueText(correction, cueTermLimit)),
		FormatCredits(applied), applied, FormatCredits(outstanding), outstanding,
		cueText(eventID, cueTermLimit), strconv.Quote(cueText(evidence, cueEvidenceLimit)))
}

// Penalties lists the identity's operator-confirmed penalties, oldest first.
func (l *Ledger) Penalties(ctx context.Context, accountID string) ([]Penalty, error) {
	state, err := unrecovered(ctx, l.db, accountID)
	if err != nil {
		return nil, err
	}
	return loadPenalties(ctx, l.db, accountID, state)
}

func loadPenalties(ctx context.Context, q querier, accountID string, state lossState) ([]Penalty, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, seq, payload, reason, at FROM events WHERE account_id = ? AND kind = ? ORDER BY seq ASC`,
		accountID, kindLoss)
	if err != nil {
		return nil, fmt.Errorf("ledger: read penalties for %s: %w", accountID, err)
	}
	defer rows.Close()
	out := make([]Penalty, 0, 4)
	for rows.Next() {
		var id, payload, reason, at string
		var seq int64
		if err := rows.Scan(&id, &seq, &payload, &reason, &at); err != nil {
			return nil, fmt.Errorf("ledger: scan penalty: %w", err)
		}
		var decoded lossPayload
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return nil, errw(ErrPayloadConflict, fmt.Errorf("loss %s payload: %w", id, err))
		}
		if !decoded.accountable() {
			continue
		}
		outstanding := state.Remaining[id]
		out = append(out, Penalty{
			EventID:       id,
			Seq:           seq,
			Disposition:   decoded.effectiveDisposition(),
			Rule:          decoded.Rule,
			Correction:    decoded.Correction,
			SelfDisclosed: decoded.SelfDisclosed,
			AppliedAmount: decoded.AppliedAmount,
			Outstanding:   outstanding,
			At:            at,
			Cue:           penaltyCue(decoded.Rule, decoded.Correction, reason, decoded.AppliedAmount, outstanding, id, decoded.SelfDisclosed),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: read penalties for %s: %w", accountID, err)
	}
	return out, nil
}
