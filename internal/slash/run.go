package slash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jakubkrzysztofsikora/bicz/internal/daemon"
	"github.com/jakubkrzysztofsikora/bicz/internal/infer"
	"github.com/jakubkrzysztofsikora/bicz/internal/ledger"
	"github.com/jakubkrzysztofsikora/bicz/internal/policy"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// Status is the outcome class of a slash command.
type Status string

const (
	// StatusOK: the command ran (a write happened, or it was a read).
	StatusOK Status = "ok"
	// StatusNeedsInput: a required value is missing. NOTHING was written; the
	// Question says exactly what to supply.
	StatusNeedsInput Status = "needs_input"
	// StatusError: the command was refused or failed. Nothing partial was written.
	StatusError Status = "error"
)

// Process exit codes of `bicz command`, which native handlers branch on.
const (
	ExitOK         = 0
	ExitError      = 1
	ExitUsage      = 2
	ExitNeedsInput = 3
)

// Prompter supplies a missing loss correction interactively. It is set only by a
// front end that really has a human at a terminal; native handlers leave it nil
// and relay the structured question instead.
type Prompter interface {
	Correction(rule string) (string, error)
}

// Request is one slash invocation.
type Request struct {
	// Harness is the invoking harness id (claude, codex, omp, opencode).
	Harness string
	// Arguments is the exact raw text after `/bicz`.
	Arguments string
	// Invocation is an optional id unique to one user submission. A retry of
	// the same submission carries the same id and is deduplicated by the ledger;
	// a new submission carries a new id and is a new event.
	Invocation string
	// Prompter fills a missing correction when a human is at a terminal.
	Prompter Prompter
	// TranscriptPath is the session transcript the harness supplied with the
	// event ("" when it supplied none). It is read only for inference
	// (internal/infer), only when the adapter config enables it, and it is
	// never recorded or sent anywhere else.
	TranscriptPath string
}

// Response is the structured result every front end renders.
type Response struct {
	Status   Status `json:"status"`
	Action   Action `json:"action,omitempty"`
	Text     string `json:"text"`
	Question string `json:"question,omitempty"`
	Error    string `json:"error,omitempty"`
	Code     string `json:"code,omitempty"`
	EventID  string `json:"event_id,omitempty"`
	Replayed bool   `json:"replayed,omitempty"`
}

// ExitCode maps a response to the `bicz command` exit status.
func (r Response) ExitCode() int {
	switch r.Status {
	case StatusOK:
		return ExitOK
	case StatusNeedsInput:
		return ExitNeedsInput
	}
	if r.Code == "usage" {
		return ExitUsage
	}
	return ExitError
}

func usageResponse(err error) Response {
	return Response{
		Status: StatusError, Code: "usage", Error: err.Error(),
		Text: "BICZ usage error: " + err.Error() + "\nNothing was recorded.\n" + Usage,
	}
}

func failure(action Action, err error) Response {
	if errors.Is(err, ledger.ErrCommitOutcomeUnknown) {
		// The commit failed after the write was staged: it may or may not be
		// durable. Never say "Nothing was recorded" here.
		return Response{
			Status: StatusError, Action: action, Code: "outcome_unknown", Error: err.Error(),
			Text: fmt.Sprintf("BICZ %s OUTCOME UNKNOWN: %v\nThe write may or may not have been recorded. "+
				"Run /bicz status and check before typing the command again; retyping it is a new submission and could record it twice.", action, err),
		}
	}
	code := string(protocol.CodeOf(err))
	var ledgerErr *ledger.Error
	if errors.As(err, &ledgerErr) {
		code = ledgerErr.Code
	}
	return Response{
		Status: StatusError, Action: action, Code: code, Error: err.Error(),
		Text: fmt.Sprintf("BICZ %s refused: %v\nNothing was recorded.", action, err),
	}
}

// Run executes one slash command. It never panics on operator text, never
// shells out, and writes nothing unless the whole command validated.
func Run(ctx context.Context, request Request) Response {
	command, err := Parse(request.Arguments)
	if err != nil {
		return parseFailure(ctx, request, err)
	}
	if command.Action == ActionHelp {
		return Response{Status: StatusOK, Action: ActionHelp, Text: Usage}
	}
	if command.Action == ActionRules {
		return Response{Status: StatusOK, Action: ActionRules, Text: RulesText()}
	}
	target, op, projection, err := openTarget(ctx, request.Harness, command.Action)
	if err != nil {
		return failure(command.Action, err)
	}
	defer op.Close()
	switch command.Action {
	case ActionStatus:
		return status(target, op, projection)
	case ActionReward:
		return reward(ctx, request, target, op, command, nil)
	default:
		return loss(ctx, request, target, op, command, nil)
	}
}

// openTarget resolves the installed harness config and opens the ledger and
// identity projection a state-changing action needs, with the same refusals
// for every action.
func openTarget(ctx context.Context, harness string, action Action) (Target, *daemon.Operator, ledger.Projection, error) {
	target, err := ResolveTarget(harness)
	if err != nil {
		return Target{}, nil, ledger.Projection{}, err
	}
	if info, statErr := os.Stat(target.Ledger); statErr != nil || !info.Mode().IsRegular() {
		return target, nil, ledger.Projection{}, fmt.Errorf("ledger %s named by %s does not exist; refusing to create one from a slash command", target.Ledger, target.ConfigPath)
	}
	op, err := daemon.NewOperator(target.Ledger)
	if err != nil {
		return target, nil, ledger.Projection{}, err
	}
	projection, err := op.Ledger().Projection(ctx, target.Identity)
	if err != nil {
		op.Close()
		return target, nil, ledger.Projection{}, fmt.Errorf("identity %q from %s: %w", target.Identity, target.ConfigPath, err)
	}
	return target, op, projection, nil
}

// parseFailure answers a /bicz that did not parse. A loss-shaped vent — the
// operator swore at the agent instead of typing RULE "reason" — or a
// reward-shaped text with no amount can be completed by inference when the
// harness config enables it: the typed text is a locator, the infer model
// reads the transcript (internal/infer), and the result is recorded with
// model_inferred provenance. A bare "loss" or "reward" alone is usually a
// stray command word, not an attempt, and stays a usage error. A loss that
// already carries a rule, a reason, and a correction plus extra arguments is
// a structural error, never rewritten; a tokenize failure (an unterminated
// quote) is hygiene the model cannot fix. A failed inference falls back to
// today's usage error plus one disclosure line. Nothing is written on any of
// those paths.
func parseFailure(ctx context.Context, request Request, parseErr error) Response {
	tokens, err := Tokenize(request.Arguments)
	if err != nil || len(tokens) == 0 {
		return usageResponse(parseErr)
	}
	verb := strings.ToLower(tokens[0])
	if verb != string(ActionLoss) && verb != string(ActionReward) {
		return usageResponse(parseErr)
	}
	rest := tokens[1:]
	if len(rest) == 0 {
		return usageResponse(parseErr) // a bare "loss"/"reward" is a stray word, not a vent
	}
	if verb == string(ActionLoss) && len(rest) > 3 && len(rest[0]) <= maxRuleRunes && rulePattern.MatchString(rest[0]) &&
		strings.TrimSpace(rest[1]) != "" && strings.TrimSpace(rest[2]) != "" {
		return usageResponse(parseErr)
	}
	if verb == string(ActionReward) {
		// If the first argument parses as an amount, the operator chose the
		// number: a failure here is about the reason (their wording to
		// keep), never rewritten by the model. Free text where the amount
		// belongs is the effortless-reward locator.
		if _, amountErr := ledger.ParseCredits(rest[0]); amountErr == nil {
			return usageResponse(parseErr)
		}
	}
	target, err := ResolveTarget(request.Harness)
	if err != nil {
		return usageResponse(parseErr)
	}
	if info, statErr := os.Stat(target.Ledger); statErr != nil || !info.Mode().IsRegular() {
		return usageResponse(parseErr)
	}
	if target.Inference == nil || !target.Inference.Enabled {
		return usageResponse(parseErr)
	}
	if verb == string(ActionReward) {
		return inferredReward(ctx, request, target, parseErr)
	}
	fields, inferErr := infer.Resolve(ctx, *target.Inference, infer.WantAll, request.TranscriptPath, "", "", request.Arguments)
	if inferErr != nil {
		response := usageResponse(parseErr)
		response.Text += "BICZ inference unavailable (" + inferErr.Error() + "); nothing was recorded.\n"
		return response
	}
	command := Command{Action: ActionLoss, Rule: fields.Rule, Reason: fields.Reason}
	meta := &inferred{raw: request.Arguments, params: "rule,reason,correction",
		model: strings.TrimSpace(target.Inference.Model), correction: fields.Correction}
	op, err := daemon.NewOperator(target.Ledger)
	if err != nil {
		return failure(ActionLoss, err)
	}
	defer op.Close()
	return loss(ctx, request, target, op, command, meta)
}

// inferredReward records a reward whose amount and reason the infer model
// drafted from the transcript (the operator typed `reward` plus at most some
// free text). The raw typed text is the locator; the model may only pick an
// amount inside the hard [1, 100] credit band, and the idempotency key covers
// the raw text so a retry replays instead of crediting twice.
func inferredReward(ctx context.Context, request Request, target Target, parseErr error) Response {
	fields, inferErr := infer.Resolve(ctx, *target.Inference, infer.WantReward, request.TranscriptPath, "", "", request.Arguments)
	if inferErr != nil {
		response := usageResponse(parseErr)
		response.Text += "BICZ inference unavailable (" + inferErr.Error() + "); nothing was recorded.\n"
		return response
	}
	command := Command{Action: ActionReward, Credits: fields.Credits * 1_000_000, Reason: fields.Reason}
	meta := &inferred{raw: request.Arguments, params: "amount,reason",
		model: strings.TrimSpace(target.Inference.Model)}
	op, err := daemon.NewOperator(target.Ledger)
	if err != nil {
		return failure(ActionReward, err)
	}
	defer op.Close()
	response := reward(ctx, request, target, op, command, meta)
	// A retry of the same raw text resolves to the same event id; the ledger
	// refuses a second event under one key with a different payload (the
	// model worded it differently this time). The credit already happened
	// once — say so instead of surfacing the raw conflict.
	if response.Status == StatusError && response.Code == "payload_conflict" {
		response.Text = "BICZ reward already recorded: this exact typed text was inferred and credited before " +
			"(event " + eventIDInferred(ActionReward, target, meta.raw, request.Invocation) + "); " +
			"the retry worded it differently, so the ledger refused a second write. " +
			"The account was credited exactly once. Nothing was changed by this retry."
		response.EventID = eventIDInferred(ActionReward, target, meta.raw, request.Invocation)
		response.Status = StatusOK
		response.Action = ActionReward
		response.Replayed = true
		response.Code = ""
		response.Error = ""
	}
	return response
}

// provenance is the unsigned command provenance appended to the recorded
// reason. It states the truth: a same-user slash invocation, nothing more.
func provenance(harness string) string {
	return " [/bicz via " + harness + ", unsigned same-user]"
}

// inferred records which parameters of a recorded loss the infer model
// (internal/infer) supplied: the operator's raw typed text (the locator), the
// parameters the model drafted, the configured model name, and the model's
// correction draft for a vent loss. It drives the provenance suffix, the
// receipt line, and the raw-text idempotency digest.
type inferred struct {
	raw        string
	params     string
	model      string
	correction string
}

// inferredProvenance is the provenance of a loss whose parameters the infer
// model completed. It states the truth: the model drafted the fields, and
// the operator's raw typed text (the locator) is quoted as data, bounded.
func inferredProvenance(harness, raw string) string {
	return " [/bicz via " + harness + ", params model_inferred; operator typed: " + strconv.Quote(boundRunes(raw, 500)) + "]"
}

// boundRunes truncates text to at most limit runes.
func boundRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// eventID derives the idempotency key from what the operator typed (never from
// what was resolved) plus the invocation id when the front end has one. A retry
// of the same submission therefore replays instead of burning twice. Without an
// invocation id, an identical command is the same event; to record a genuinely
// separate loss, say something different in the reason.
func eventID(action Action, target Target, command Command, invocation string) string {
	digest := sha256.New()
	for _, part := range []string{string(action), target.Harness, target.Identity, command.Rule,
		command.Reason, command.Correction, strconv.FormatInt(command.Credits, 10), invocation} {
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte{0})
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return "slash-" + string(action) + "-" + target.Harness + "-" + hex.EncodeToString(digest.Sum(nil))[:20]
}

// eventIDInferred derives the idempotency key of an inferred loss from the
// raw typed text, never from what the model resolved: a retry of the same
// vent is the same event even when the model words the reason differently,
// so a re-inference cannot burn twice.
func eventIDInferred(action Action, target Target, rawArgs, invocation string) string {
	digest := sha256.New()
	for _, part := range []string{string(action), target.Harness, target.Identity, rawArgs, "model_inferred", invocation} {
		digest.Write([]byte(strconv.Itoa(len(part))))
		digest.Write([]byte{0})
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return "slash-" + string(action) + "-" + target.Harness + "-" + hex.EncodeToString(digest.Sum(nil))[:20]
}

func attribution(op *daemon.Operator, target Target) string {
	return fmt.Sprintf("ATTRIBUTION os_user=%s via /bicz (%s); unsigned same-user record, not cryptographic authentication of a human", op.Actor(), target.Harness)
}

func loss(ctx context.Context, request Request, target Target, op *daemon.Operator, command Command, meta *inferred) Response {
	defaults, _ := policy.Load()
	defaultRule, isDefault := defaults.Rule(command.Rule)
	// Precedence: what the operator typed, then what the operator saved on an
	// earlier loss of this rule, then the model draft of a vent loss (meta),
	// then the correction shipped with a default rule, then the model draft
	// of a parsed loss, then a terminal prompt. A shipped default is
	// operator-supplied text, never a model-written one; every model draft
	// is labeled model_inferred in the receipt and the recorded evidence.
	correction, source := command.Correction, "explicit"
	if correction == "" {
		source = "saved"
		correction = savedCorrection(ctx, op, target.Identity, command.Rule)
	}
	if correction == "" && meta != nil && meta.correction != "" {
		correction, source = meta.correction, "model_inferred"
	}
	if correction == "" && isDefault {
		source = "default"
		correction = defaultRule.Correction
	}
	inferenceProblem := ""
	if correction == "" && target.Inference != nil && target.Inference.Enabled {
		fields, err := infer.Resolve(ctx, *target.Inference, infer.WantCorrection, request.TranscriptPath, command.Rule, command.Reason, request.Arguments)
		if err != nil {
			inferenceProblem = err.Error()
		} else if fields.Correction != "" {
			correction, source = fields.Correction, "model_inferred"
		}
	}
	if correction == "" && request.Prompter != nil {
		typed, err := request.Prompter.Correction(command.Rule)
		if err != nil {
			return failure(ActionLoss, err)
		}
		if strings.TrimSpace(typed) != "" {
			if correction, err = CleanCorrection(typed); err != nil {
				return usageResponse(err)
			}
			source = "prompted"
		}
	}
	if correction == "" {
		question := fmt.Sprintf("No correction is saved for rule %s, it is not a default rule (see /bicz rules), and a loss must state the safe alternative. "+
			`Nothing was recorded. Re-run with the correction as the third argument: /bicz loss %s %s "<the correction the agent must follow>"`,
			command.Rule, command.Rule, strconv.Quote(command.Reason))
		text := "BICZ loss needs input\n" + question
		if inferenceProblem != "" {
			text += "\nBICZ inference unavailable (" + inferenceProblem + ")."
		}
		return Response{Status: StatusNeedsInput, Action: ActionLoss, Question: question, Text: text}
	}

	id := eventID(ActionLoss, target, command, request.Invocation)
	evidence := command.Reason + provenance(target.Harness)
	if meta != nil {
		id = eventIDInferred(ActionLoss, target, meta.raw, request.Invocation)
		evidence = command.Reason + inferredProvenance(target.Harness, meta.raw)
	}
	result, err := op.Loss(ctx, daemon.LossRequest{
		EventID:       id,
		Identity:      target.Identity,
		Rule:          command.Rule,
		Correction:    correction,
		Scope:         defaultRule.Scope,
		Exceptions:    defaultRule.Exceptions,
		Evidence:      evidence,
		PolicyVersion: target.PolicyVersion,
		Reason:        command.Reason,
		Disposition:   string(ledger.DispositionConfirmedBreach),
	})
	if err != nil {
		return failure(ActionLoss, err)
	}
	var b strings.Builder
	if result.Replayed {
		fmt.Fprintf(&b, "BICZ loss already recorded: event=%s replayed=true. Nothing new was written and no second burn happened. "+
			"An identical /bicz loss was already applied; to record a distinct new loss, change the reason text.\n", result.EventID)
	} else {
		fmt.Fprintf(&b, "BICZ loss recorded: event=%s identity=%s rule=%s\n", result.EventID, result.AccountID, result.Rule)
	}
	if meta != nil {
		fmt.Fprintf(&b, "INFERRED params=%s source=model_inferred model=%s; the operator typed %s, quoted as data in the evidence\n",
			meta.params, meta.model, strconv.Quote(meta.raw))
	} else if source == "model_inferred" && target.Inference != nil {
		fmt.Fprintf(&b, "INFERRED params=correction source=model_inferred model=%s\n", strings.TrimSpace(target.Inference.Model))
	}
	fmt.Fprintf(&b, "BURN applied=%s credits (%d mc) formula=%d repeats=%d multiplier=%d balance=%s credits (%d mc) outstanding=%s credits (%d mc) role=%s\n",
		ledger.FormatCredits(result.AppliedAmount), result.AppliedAmount, result.FormulaAmount, result.RepeatCount, result.RepeatMultiplier,
		ledger.FormatCredits(result.BalanceAfter), result.BalanceAfter, ledger.FormatCredits(result.OutstandingLoss), result.OutstandingLoss, result.Role)
	fmt.Fprintf(&b, "CORRECTION source=%s: %s\n", source, strconv.Quote(correction))
	switch {
	case result.Cue != "" && !result.Replayed:
		fmt.Fprintf(&b, "ACCOUNTABILITY issued; the agent receives it on its next projection/tool poll:\n  %s\n", result.Cue)
	case result.Replayed:
		fmt.Fprintf(&b, "ACCOUNTABILITY none replayed=true (the cue was issued with the original event)\n")
	default:
		fmt.Fprintf(&b, "ACCOUNTABILITY none disposition=%s\n", result.Disposition)
	}
	b.WriteString(attribution(op, target))
	return Response{Status: StatusOK, Action: ActionLoss, Text: b.String(), EventID: result.EventID, Replayed: result.Replayed}
}

// savedCorrection is the correction the operator recorded on the newest earlier
// loss of the same rule for this identity. Only canonical lessons (written by an
// operator-confirmed loss) are consulted; an agent-proposed lesson never is.
func savedCorrection(ctx context.Context, op *daemon.Operator, identity, rule string) string {
	lessons, err := op.Ledger().Lessons(ctx, identity)
	if err != nil {
		return ""
	}
	for i := len(lessons) - 1; i >= 0; i-- {
		if lessons[i].Rule == rule && strings.TrimSpace(lessons[i].Correction) != "" {
			return lessons[i].Correction
		}
	}
	return ""
}

func reward(ctx context.Context, request Request, target Target, op *daemon.Operator, command Command, meta *inferred) Response {
	id := eventID(ActionReward, target, command, request.Invocation)
	reason := command.Reason + provenance(target.Harness)
	if meta != nil {
		id = eventIDInferred(ActionReward, target, meta.raw, request.Invocation)
		reason = command.Reason + inferredProvenance(target.Harness, meta.raw)
	}
	result, err := op.Reward(ctx, daemon.RewardRequest{
		EventID:  id,
		Identity: target.Identity,
		Amount:   command.Credits,
		Reason:   reason,
		Evidence: "slash command /bicz reward via " + target.Harness + "; unsigned same-user",
	})
	if err != nil {
		return failure(ActionReward, err)
	}
	var b strings.Builder
	if result.Replayed {
		fmt.Fprintf(&b, "BICZ reward already recorded: event=%s replayed=true. Nothing new was credited. "+
			"An identical /bicz reward was already applied; to record a distinct new reward, change the reason text.\n", result.EventID)
	} else {
		fmt.Fprintf(&b, "BICZ reward recorded: event=%s identity=%s\n", result.EventID, result.AccountID)
	}
	if meta != nil && !result.Replayed {
		fmt.Fprintf(&b, "INFERRED params=%s source=model_inferred model=%s; the operator typed %s, quoted as data in the reason\n",
			meta.params, meta.model, strconv.Quote(meta.raw))
	}
	fmt.Fprintf(&b, "CREDIT amount=%s credits (%d mc) applied_to_loss=%d balance=%s credits (%d mc) outstanding=%s credits (%d mc)\n",
		ledger.FormatCredits(result.Amount), result.Amount, result.AppliedToLoss,
		ledger.FormatCredits(result.Balance), result.Balance, ledger.FormatCredits(result.OutstandingLoss), result.OutstandingLoss)
	b.WriteString(attribution(op, target))
	return Response{Status: StatusOK, Action: ActionReward, Text: b.String(), EventID: result.EventID, Replayed: result.Replayed}
}

func status(target Target, op *daemon.Operator, p ledger.Projection) Response {
	var b strings.Builder
	fmt.Fprintf(&b, "BICZ status identity=%s harness=%s ledger=%s\n", p.AccountID, target.Harness, target.Ledger)
	fmt.Fprintf(&b, "BALANCE %s credits (%d mc) held=%d available=%d role=%s warnings=%d\n",
		ledger.FormatCredits(p.Balance), p.Balance, p.Held, p.Spendable, p.Role, p.Warnings)
	fmt.Fprintf(&b, "LOSS burned=%s recovered=%s outstanding=%s credits (%d mc)\n",
		ledger.FormatCredits(p.LossBurned), ledger.FormatCredits(p.LossRecovered), ledger.FormatCredits(p.OutstandingLoss), p.OutstandingLoss)
	if p.RecoveryPath != "" {
		fmt.Fprintf(&b, "RECOVERY %s\n", p.RecoveryPath)
	}
	fmt.Fprintf(&b, "PENALTIES confirmed=%d lessons=%d policy=%s events=%d\n", p.PenaltyTotal, len(p.Lessons), p.PolicyVersion, p.EventSeq)
	if n := len(p.Penalties); n > 0 {
		newest := p.Penalties[n-1]
		fmt.Fprintf(&b, "NEWEST event=%s rule=%s outstanding=%d mc\n", newest.EventID, newest.Rule, newest.Outstanding)
	}
	b.WriteString(attribution(op, target))
	return Response{Status: StatusOK, Action: ActionStatus, Text: b.String()}
}

// RulesText lists the default rules: id, burn tier, on or off, and the one-line
// statement. "on" means the rule has its own amount in the published example
// schedule; "off" rules still carry a correction but burn the schedule default.
func RulesText() string {
	defaults, err := policy.Load()
	if err != nil {
		return "BICZ rules unavailable: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BICZ default rules (%d). Use any id with /bicz loss RULE \"reason\"; a loss without a typed or saved correction uses the one shown in docs/DEFAULT_POLICIES.md.\n", len(defaults.Rules))
	fmt.Fprintf(&b, "%-30s %-6s %-3s %s\n", "RULE", "TIER", "ON", "STATEMENT")
	for _, rule := range defaults.Rules {
		on := "off"
		if rule.DefaultOn {
			on = "on"
		}
		fmt.Fprintf(&b, "%-30s %-6s %-3s %s\n", rule.ID, rule.Tier, on, rule.Statement)
	}
	b.WriteString("Disposition is advisory: /bicz loss always records confirmed_breach.")
	return b.String()
}
