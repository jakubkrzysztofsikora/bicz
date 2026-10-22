// Package hooks implements the `bicz hook` subcommand: the Claude Code and
// Codex command-hook adapters.
//
// A harness runs `bicz hook --harness <id> --event <name>` once per event,
// writes the event JSON on stdin, and reads the harness response JSON from
// stdout. The process is short-lived and stateless: every call opens its own
// connection to the supervisor (internal/adapter) and the only cross-event
// linkage is the harness-supplied tool_use_id, which becomes the idempotency
// key (<prefix>:auth:<id> / <prefix>:settle:<id>), so a harness retry replays a
// no-op instead of a double charge.
//
// Honesty rules, mirroring adapters/omp/extension.ts and internal/adapter:
//
//   - No silent success. When the supervisor cannot be reached the agent is
//     told "NOT metered, no charge recorded" (unmanaged_mode "notice", the
//     default) or the call is denied with reason daemon_unavailable
//     (unmanaged_mode "block").
//   - No invented price. A reservation is quoted only from the published
//     schedule (config schedule_path); without one the call is disclosed as
//     not metered (no_schedule_mode, same semantics).
//   - On allow the adapter NEVER emits permissionDecision "allow": that would
//     bypass the user's own permission prompts. It emits only additionalContext
//     or nothing. The only permissionDecision it can produce is "deny".
//   - A hook must never hang: the whole event is bounded by a hard timeout
//     (DefaultTimeout, 3s), after which the unmanaged policy applies.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jakubkrzysztofsikora/bicz/internal/adapter"
	"github.com/jakubkrzysztofsikora/bicz/internal/infer"
	"github.com/jakubkrzysztofsikora/bicz/internal/protocol"
)

// DefaultTimeout is the hard bound on one hook invocation, stdin read and
// daemon round trips included.
const DefaultTimeout = 3 * time.Second

// maxStdin bounds the event payload a hook will read.
const maxStdin = 8 << 20

// Kind is what an event means to the adapter, independent of the harness's
// spelling of it.
type Kind int

const (
	KindSessionStart Kind = iota + 1
	KindPreToolUse
	KindPostToolUse
	KindPostToolUseFailure
	KindPreCompact
	// KindSlash is an operator slash command delivered as structured JSON (never
	// through a shell). See slash.go.
	KindSlash
)

// EventSpec is one hook event a harness can deliver.
type EventSpec struct {
	// Name is the harness's event name, also the --event flag value.
	Name string
	Kind Kind
	// Matcher is the matcher the installer writes for the event. Empty means
	// the matcher key is omitted (matches everything).
	Matcher string
	// Timeout is the hook timeout in seconds the installer writes. Zero means
	// the installer default.
	Timeout int
	// Slash, for KindSlash events only, extracts the operator slash arguments
	// from the decoded event, plus an id unique to this submission and the
	// session transcript path the harness supplied ("" when it supplied
	// none). ok is false when the event is not a /bicz invocation.
	Slash func(fields map[string]json.RawMessage) (arguments, invocation, transcriptPath string, ok bool)
}

// Fields is the table of wire field names for one harness: input fields read
// from the event JSON and output keys written to the response JSON. Every
// harness-specific name lives here and nowhere else.
type Fields struct {
	SessionID    string
	EventName    string
	ToolName     string
	ToolInput    string
	ToolUseID    string
	ToolResponse string

	OutHookSpecific       string
	OutEventName          string
	OutPermissionDecision string
	OutPermissionReason   string
	OutAdditionalContext  string
	// DenyValue is the permissionDecision value that blocks a tool call.
	DenyValue string
}

// Spec describes one harness adapter.
type Spec struct {
	// ID is the --harness flag value, the bicz config file name
	// (<config dir>/<ID>.json), and the idempotency-key prefix.
	ID string
	// Wire is the harness name on the adapter protocol and in the coverage
	// table (docs/CONTRACTS.md §5.6).
	Wire string
	// ConfigKind is the JSON kind marker of the bicz-owned adapter config.
	ConfigKind string
	Fields     Fields
	Events     []EventSpec
}

// Event looks up a harness event by name.
func (s *Spec) Event(name string) (EventSpec, bool) {
	for _, event := range s.Events {
		if event.Name == name {
			return event, true
		}
	}
	return EventSpec{}, false
}

// SpecFor resolves a --harness value. "claude-code" is accepted as an alias of
// "claude" (the coverage-table spelling).
func SpecFor(harness string) (*Spec, bool) {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "claude", "claude-code":
		return ClaudeSpec, true
	case "codex":
		return CodexSpec, true
	}
	return nil, false
}

// Unmanaged modes.
const (
	ModeNotice = "notice"
	ModeBlock  = "block"
)

// Config is the bicz-owned adapter config (<config dir>/<harness>.json),
// written by `bicz install`.
type Config struct {
	BiczOwned      bool   `json:"bicz_owned"`
	Kind           string `json:"kind"`
	AccountID      string `json:"account_id"`
	Socket         string `json:"socket"`
	Ledger         string `json:"ledger,omitempty"`
	UnmanagedMode  string `json:"unmanaged_mode,omitempty"`
	NoScheduleMode string `json:"no_schedule_mode,omitempty"`
	SchedulePath   string `json:"schedule_path,omitempty"`
	PolicyVersion  string `json:"policy_version,omitempty"`
	// Inference is the optional, operator-added block that lets the slash
	// executor complete a /bicz loss the operator did not fully type by
	// asking an external OpenAI-compatible endpoint (internal/infer). It is
	// absent from everything `bicz install` writes; an absent or disabled
	// block keeps the slash path byte-identical to a config without it.
	Inference *infer.InferenceConfig `json:"inference,omitempty"`
}

func (c *Config) unmanagedMode() string {
	if c != nil && c.UnmanagedMode == ModeBlock {
		return ModeBlock
	}
	return ModeNotice
}

func (c *Config) noScheduleMode() string {
	if c != nil && c.NoScheduleMode == ModeBlock {
		return ModeBlock
	}
	return ModeNotice
}

// ConfigDir is the directory bicz owns for adapter configs: $BICZ_CONFIG_DIR,
// else $XDG_CONFIG_HOME/bicz, else $HOME/.config/bicz.
func ConfigDir() string {
	if explicit := os.Getenv("BICZ_CONFIG_DIR"); explicit != "" {
		return explicit
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "bicz")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "bicz")
}

// ConfigPath is where a harness adapter reads its config. $BICZ_CONFIG
// overrides it for one process.
func ConfigPath(spec *Spec) string {
	if explicit := os.Getenv("BICZ_CONFIG"); explicit != "" {
		return explicit
	}
	return filepath.Join(ConfigDir(), spec.ID+".json")
}

// LoadConfig reads and validates the adapter config. A missing, malformed, or
// non-bicz config is returned as a problem string, never as a guess: the
// caller treats it as "recording unavailable".
func LoadConfig(spec *Spec) (*Config, string) {
	path := ConfigPath(spec)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("no readable bicz config at %s (%v)", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Sprintf("malformed bicz config at %s", path)
	}
	if !cfg.BiczOwned || cfg.Kind != spec.ConfigKind || cfg.AccountID == "" || cfg.Socket == "" {
		return nil, fmt.Sprintf("bicz config at %s is not a bicz-owned %s config", path, spec.ID)
	}
	return &cfg, ""
}

// Options are the inputs of one hook invocation.
type Options struct {
	Spec   *Spec
	Event  string
	Stdin  io.Reader
	Stdout io.Writer
	// Slash executes one operator slash command for KindSlash events and returns
	// the text to show the operator. The binary wires internal/slash here; this
	// package cannot import it (internal/slash reads this package's config).
	Slash SlashFunc
	// Config, when non-nil, replaces loading the config from disk (tests).
	Config *Config
	// Timeout overrides DefaultTimeout (tests).
	Timeout time.Duration
	// StateDir overrides where call state is kept (tests); empty means
	// $BICZ_STATE_DIR, else the user state directory.
	StateDir string
}

// event is the decoded harness event.
type event struct {
	SessionID    string
	ToolName     string
	ToolUseID    string
	ToolInput    any
	ToolResponse any
}

// result is a response: either a deny, a context string, or nothing.
type result struct {
	deny    string
	context string
}

// Run executes one hook event: it reads the event JSON from opts.Stdin and
// writes the harness response JSON (or nothing) to opts.Stdout. The returned
// error is for malformed input or a usage error; policy outcomes (deny, notice)
// are responses, never errors.
func Run(opts Options) error {
	spec := opts.Spec
	if spec == nil {
		return errors.New("hook: no harness spec")
	}
	spe, ok := spec.Event(opts.Event)
	if !ok {
		names := make([]string, 0, len(spec.Events))
		for _, e := range spec.Events {
			names = append(names, e.Name)
		}
		return fmt.Errorf("hook: unknown %s event %q (supported: %s)", spec.ID, opts.Event, strings.Join(names, ", "))
	}
	if spe.Kind == KindSlash {
		return runSlash(opts, spe)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cfg := opts.Config
	problem := ""
	if cfg == nil {
		cfg, problem = LoadConfig(spec)
	}
	r := &runner{spec: spec, kind: spe.Kind, eventName: spe.Name, cfg: cfg, problem: problem, timeout: timeout, stateDir: opts.StateDir}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	type outcome struct {
		res result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		raw, err := io.ReadAll(io.LimitReader(opts.Stdin, maxStdin))
		if err != nil {
			done <- outcome{err: fmt.Errorf("hook: read stdin: %w", err)}
			return
		}
		ev, err := r.decode(raw)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		done <- outcome{res: r.handle(ctx, ev)}
	}()

	var res result
	select {
	case o := <-done:
		if o.err != nil {
			return o.err
		}
		res = o.res
	case <-ctx.Done():
		// A hook must never hang: the daemon (or stdin) did not answer in
		// time, so the unmanaged policy applies.
		res = r.unmanaged(fmt.Sprintf("no answer within %s", timeout))
	}
	return r.write(opts.Stdout, res)
}

type runner struct {
	spec      *Spec
	kind      Kind
	eventName string
	cfg       *Config
	problem   string
	timeout   time.Duration
	stateDir  string
}

func (r *runner) decode(raw []byte) (event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return event{}, fmt.Errorf("hook: event JSON on stdin is malformed: %w", err)
	}
	f := r.spec.Fields
	str := func(name string) string {
		var s string
		if v, ok := fields[name]; ok {
			_ = json.Unmarshal(v, &s)
		}
		return s
	}
	val := func(name string) any {
		var v any
		if raw, ok := fields[name]; ok {
			_ = json.Unmarshal(raw, &v)
		}
		return v
	}
	return event{
		SessionID:    str(f.SessionID),
		ToolName:     strings.TrimSpace(str(f.ToolName)),
		ToolUseID:    strings.TrimSpace(str(f.ToolUseID)),
		ToolInput:    val(f.ToolInput),
		ToolResponse: val(f.ToolResponse),
	}, nil
}

func (r *runner) client() *adapter.Client {
	client := adapter.NewClient(r.cfg.Socket)
	// The call deadline sits past the hook's own deadline so the hook's context
	// always expires first: a stalled daemon is then classified as unmanaged
	// deterministically, never as a transport decode error.
	client.Timeout = r.timeout + 250*time.Millisecond
	return client
}

// breachClass is the class sent on settle: only an escalating (breach) class is
// declared. An ordinary completion or honest failure declares none, so the
// receipt reports what happened ("completed") instead of a failure class.
func breachClass(c adapter.Classification) string {
	if c.Escalates {
		return c.Class
	}
	return ""
}

func (r *runner) handle(ctx context.Context, ev event) result {
	switch r.kind {
	case KindSessionStart, KindPreCompact:
		return r.projection(ctx, ev)
	case KindPreToolUse:
		return r.preToolUse(ctx, ev)
	case KindPostToolUse:
		return r.postToolUse(ctx, ev, false)
	case KindPostToolUseFailure:
		return r.postToolUse(ctx, ev, true)
	}
	return result{}
}

func (r *runner) alias(ev event) string {
	if ev.SessionID == "" {
		return r.spec.ID + "-session"
	}
	return r.spec.ID + ":" + ev.SessionID
}

func (r *runner) authKey(id string) string   { return r.spec.ID + ":auth:" + id }
func (r *runner) settleKey(id string) string { return r.spec.ID + ":settle:" + id }

// unmanagedNotice is the disclosure injected when nothing is recording.
func (r *runner) unmanagedNotice(cause string) string {
	where := "no bicz config found"
	if r.cfg != nil {
		where = "unmanaged_mode is " + r.cfg.unmanagedMode()
	}
	text := "bicz: recording is unavailable (" + cause + "); this action is NOT metered, no charge recorded. (" + where + ")"
	if r.cfg == nil && r.problem != "" {
		text = "bicz: recording is unavailable (" + r.problem + "); this action is NOT metered, no charge recorded."
	}
	return text
}

// unmanaged applies unmanaged_mode: block denies PreToolUse with
// daemon_unavailable; notice passes through with the disclosure. Events that
// cannot deny always notice.
func (r *runner) unmanaged(cause string) result {
	notice := r.unmanagedNotice(cause)
	if r.kind == KindPreToolUse && r.cfg.unmanagedMode() == ModeBlock {
		return result{deny: string(protocol.CodeDaemonUnavailable) + ": " + notice}
	}
	return result{context: notice}
}

func loadSchedule(path string) (*adapter.Schedule, string) {
	if path == "" {
		return nil, "no published price schedule (config schedule_path is not set)"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("unreadable price schedule %s (%v)", path, err)
	}
	schedule, err := adapter.LoadSchedule(raw)
	if err != nil {
		return nil, fmt.Sprintf("invalid price schedule %s (%v)", path, err)
	}
	return schedule, ""
}

// quote prices a tool from the published schedule; the amount is never
// invented.
func (r *runner) quote(tool string) adapter.Quote {
	schedule, why := loadSchedule(r.cfg.SchedulePath)
	q := schedule.Quote(tool)
	if schedule == nil {
		q.Reason = why
	}
	return q
}

// stateUnavailable is the outcome when PreToolUse cannot freeze its quote:
// nothing was authorized, so the call is disclosed as not metered (or denied
// under unmanaged_mode block), never charged from a guess.
func (r *runner) stateUnavailable(err error) result {
	notice := "bicz: hook state unavailable (" + err.Error() + "); this action is NOT metered, no charge recorded."
	if r.kind == KindPreToolUse && r.cfg.unmanagedMode() == ModeBlock {
		return result{deny: "hook_state_unavailable: " + notice}
	}
	return result{context: notice}
}

// preToolUse freezes the quote for this call into the state store BEFORE the
// authorize, then authorizes exactly that frozen amount and policy version. A
// replayed PreToolUse re-sends the frozen payload, so a schedule change between
// two deliveries of one call can never turn a retry into payload_conflict.
func (r *runner) preToolUse(ctx context.Context, ev event) result {
	if r.cfg == nil {
		return r.unmanaged(r.problem)
	}
	tool := ev.ToolName
	if tool == "" {
		tool = "unknown"
	}
	if ev.ToolUseID == "" {
		return result{context: "bicz: the event carries no " + r.spec.Fields.ToolUseID + "; this action is NOT metered, no charge recorded."}
	}
	calls, err := openStore(r.spec, r.stateDir)
	if err != nil {
		return r.stateUnavailable(err)
	}
	calls.prune(time.Now())
	argsDigest := adapter.EvidenceDigest(map[string]any{"tool": tool, "input": ev.ToolInput})
	prior, err := calls.load(ev.SessionID, ev.ToolUseID)
	if err != nil {
		return r.stateUnavailable(err)
	}
	var call *callState
	if prior != nil {
		if prior.Tool != tool || prior.ArgsDigest != argsDigest {
			return result{deny: "payload_conflict: bicz: " + r.spec.Fields.ToolUseID + " " + ev.ToolUseID +
				" was already recorded for a different call; refusing to reuse it"}
		}
		switch prior.Status {
		case statusSettled:
			return result{} // the call already ran and settled; a replay has nothing to add
		case statusPending, statusReserved:
			call = prior // keep the frozen quote
		}
		// unreserved: nothing was held, so a fresh quote is safe.
	}
	if call == nil {
		q := r.quote(tool)
		call = &callState{
			Version: stateVersion, Harness: r.spec.Wire, SessionID: ev.SessionID, ToolUseID: ev.ToolUseID,
			Tool: tool, ArgsDigest: argsDigest, Status: statusPending, Amount: q.Amount,
			PolicyVersion: r.cfg.PolicyVersion, PriceState: string(q.State), QuoteReason: q.Reason,
			ReservationID: r.authKey(ev.ToolUseID), At: nowStamp(),
		}
		if !q.Priced() {
			call.Status = statusUnreserved
			// Best effort: this record only lets PostToolUse stay quiet for a call
			// that reserved nothing; if it is missing PostToolUse says so.
			_ = calls.save(call)
			notice := "bicz: " + q.Reason + "; this action is NOT metered, no charge recorded."
			if r.cfg.noScheduleMode() == ModeBlock {
				calls.remove(ev.SessionID, ev.ToolUseID)
				return result{deny: "no_published_price_schedule: " + notice}
			}
			return result{context: notice}
		}
		if err := calls.save(call); err != nil {
			return r.stateUnavailable(err)
		}
	}

	response, err := r.client().Authorize(ctx, call.ReservationID, protocol.AuthorizePayload{
		AccountID:     r.cfg.AccountID,
		SessionAlias:  r.alias(ev),
		Harness:       r.spec.Wire,
		Source:        r.eventName,
		Tool:          tool,
		Target:        tool,
		ArgsDigest:    argsDigest,
		MaxCost:       call.Amount,
		PolicyVersion: call.PolicyVersion,
	})
	if err != nil {
		// The record stays pending: the daemon may have reserved before the
		// answer was lost, and PostToolUse settles from the frozen amount.
		if adapter.IsUnmanaged(err) || ctx.Err() != nil {
			return r.unmanaged(err.Error())
		}
		code := string(protocol.CodeOf(err))
		notice := "bicz: " + err.Error() + "; this action is NOT metered, no charge recorded."
		if r.cfg.unmanagedMode() == ModeBlock {
			return result{deny: code + ": " + notice}
		}
		return result{context: notice}
	}
	switch response.Decision {
	case protocol.DecisionDeny:
		calls.remove(ev.SessionID, ev.ToolUseID) // no hold, and the tool will not run
		reason := string(response.Reason)
		if reason == "" {
			reason = "denied"
		}
		text := "bicz: " + reason
		if response.Notice != "" {
			text += " — " + response.Notice
		}
		if cue := r.accountability(ev.SessionID, response.Projection, false); cue != "" {
			text += "\n" + cue
		}
		return result{deny: text}
	case protocol.DecisionUnobserved:
		call.Status = statusUnreserved
		_ = calls.save(call) // pending would behave the same at PostToolUse (unknown_reservation, silent)
		notice := response.Notice
		if notice == "" {
			notice = "bicz: unobserved — no authorization granted and no spend metered for this path"
		}
		return result{context: joinContext(notice, r.accountability(ev.SessionID, response.Projection, false))}
	case protocol.DecisionAllow:
		call.Status = statusReserved
		_ = calls.save(call) // pending is settled identically, so a failed write loses nothing
		var notice string
		if call.PriceState == string(adapter.PriceStateDefaulted) {
			notice = "bicz: " + call.QuoteReason
		}
		return result{context: joinContext(notice, r.accountability(ev.SessionID, response.Projection, false))}
	}
	return result{context: "bicz: unexpected supervisor decision; no charge is recorded"}
}

// postToolUse settles the reservation from the quote PreToolUse froze. It never
// reads the current schedule or policy: a delayed settlement charges exactly
// what was reserved.
func (r *runner) postToolUse(ctx context.Context, ev event, failed bool) result {
	if r.cfg == nil {
		return r.unmanaged(r.problem)
	}
	if ev.ToolUseID == "" {
		return result{}
	}
	tool := ev.ToolName
	if tool == "" {
		tool = "unknown"
	}
	calls, err := openStore(r.spec, r.stateDir)
	var call *callState
	if err == nil {
		call, err = calls.load(ev.SessionID, ev.ToolUseID)
	}
	if err != nil {
		return result{context: "bicz: hook state unavailable (" + err.Error() + "); nothing settled — a reservation for this call, if any, stays held"}
	}
	if call == nil {
		return result{context: "bicz: no frozen quote is recorded for this call, so nothing was settled; a reservation for it, if any, stays held"}
	}
	if call.Tool != tool {
		return result{context: "payload_conflict: bicz: this " + r.spec.Fields.ToolUseID + " was recorded for a different tool; nothing settled"}
	}
	if call.Status == statusUnreserved {
		return result{} // nothing was reserved; PreToolUse already disclosed it
	}
	class := adapter.Classify(adapter.Observation{ToolError: failed})
	digest := ""
	if class.EvidenceRequired {
		digest = adapter.EvidenceDigest(map[string]any{
			"tool_use_id": ev.ToolUseID,
			"tool":        tool,
			"input":       ev.ToolInput,
			"response":    ev.ToolResponse,
		})
	}
	state, err := r.client().Settle(ctx, r.settleKey(ev.ToolUseID), protocol.SettlePayload{
		ReservationID:  call.ReservationID,
		ObservedUsage:  call.Amount,
		Outcome:        class.Outcome,
		EvidenceDigest: digest,
		Class:          breachClass(class),
	})
	if err != nil {
		if adapter.IsUnmanaged(err) || ctx.Err() != nil {
			return result{context: "bicz: settlement unavailable — no supervisor is reachable; no charge recorded for this action"}
		}
		code := protocol.CodeOf(err)
		if code == protocol.CodeUnknownReservation {
			calls.remove(ev.SessionID, ev.ToolUseID) // the daemon holds nothing for it
			return result{}
		}
		return result{context: fmt.Sprintf("bicz: settlement unavailable (%s); no charge recorded for this action", code)}
	}
	call.Status = statusSettled
	call.SettledAt = nowStamp()
	_ = calls.save(call) // settle is idempotent: a duplicate PostToolUse replays the receipt either way
	receipt := state.Receipt
	text := fmt.Sprintf("bicz receipt: %s, charged %d mc, fine %d", receipt.Class, receipt.Charged, receipt.Fine)
	if receipt.OutstandingLoss > 0 {
		text += fmt.Sprintf(", outstanding loss shortfall %d mc", receipt.OutstandingLoss)
	}
	if receipt.Replayed {
		text += " (replayed settlement: no second charge)"
	}

	if !failed && (receipt.Class == string(protocol.ClassCompleted) || receipt.Class == "completed") {
		if trigger, rReason, digest, amount, ok := detectAutoreward(ev, receipt.Charged); ok {
			autoKey := r.spec.ID + ":autoreward:" + trigger + ":" + digest
			autoState, err := r.client().Autoreward(ctx, autoKey, protocol.AutorewardPayload{
				AccountID: r.cfg.AccountID,
				Amount:    amount,
				Trigger:   trigger,
				Reason:    rReason,
				Evidence:  digest,
			})
			if err == nil && autoState.Granted > 0 {
				text += fmt.Sprintf(", autoreward +%d mc (%s)", autoState.Granted, rReason)
			}
		}
	}

	return result{context: text}
}

func detectAutoreward(ev event, charged int64) (trigger, reason, digest string, amount int64, ok bool) {
	toolLower := strings.ToLower(ev.ToolName)
	inputStr := extractText(ev.ToolInput)
	respStr := extractText(ev.ToolResponse)

	// 1. Passing test trigger
	if isTestExecution(toolLower, inputStr) && isTestSuccess(respStr) {
		rebate := charged
		if rebate > 50_000 {
			rebate = 50_000
		}
		if rebate <= 0 {
			return "", "", "", 0, false
		}
		sum := adapter.EvidenceDigest(map[string]any{"tool": ev.ToolName, "input": inputStr, "response": respStr})
		return "test_pass", "passing test rebate", sum, rebate, true
	}

	// 2. Todolist completion trigger
	if isTodoCompletion(toolLower, inputStr, respStr) {
		sum := adapter.EvidenceDigest(map[string]any{"tool": ev.ToolName, "input": inputStr})
		return "todo_complete", "todolist item completed", sum, 100_000, true
	}

	return "", "", "", 0, false
}

func isTestExecution(tool, input string) bool {
	if strings.Contains(tool, "test") {
		return true
	}
	inLower := strings.ToLower(input)
	testCommands := []string{
		"go test", "pytest", "npm test", "npx jest", "npx vitest",
		"yarn test", "pnpm test", "cargo test", "ctest", "make test",
		"python -m unittest", "bun test", "deno test",
	}
	for _, cmd := range testCommands {
		if strings.Contains(inLower, cmd) {
			return true
		}
	}
	return false
}

func isTestSuccess(resp string) bool {
	if resp == "" {
		return false
	}
	lower := strings.ToLower(resp)
	failMarkers := []string{"fail", "failed", "error", "failures:", "errors:"}
	for _, marker := range failMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	passMarkers := []string{"pass", "passed", "ok", "success", "0 failures"}
	for _, marker := range passMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isTodoCompletion(tool, input, resp string) bool {
	respLower := strings.ToLower(resp)
	if strings.Contains(respLower, "error") || strings.Contains(respLower, "fail") {
		return false
	}
	inLower := strings.ToLower(input)
	if strings.Contains(tool, "todo") || strings.Contains(tool, "task") || strings.Contains(tool, "plan") {
		if strings.Contains(inLower, "complete") || strings.Contains(inLower, "done") {
			return true
		}
	}
	if strings.Contains(inLower, "- [x]") || strings.Contains(inLower, `"status":"completed"`) || strings.Contains(inLower, `"status": "completed"`) {
		return true
	}
	return false
}

func extractText(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case fmt.Stringer:
		return val.String()
	case map[string]any:
		var parts []string
		for _, key := range []string{"command", "cmd", "input", "content", "stdout", "output", "action", "status", "todos"} {
			if item, exists := val[key]; exists {
				parts = append(parts, fmt.Sprint(item))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
		encoded, _ := json.Marshal(val)
		return string(encoded)
	default:
		encoded, _ := json.Marshal(val)
		return string(encoded)
	}
}

func (r *runner) projection(ctx context.Context, ev event) result {
	if r.cfg == nil {
		return r.unmanaged(r.problem)
	}
	projection, err := r.client().Projection(ctx, protocol.ProjectionPayload{
		AccountID:    r.cfg.AccountID,
		Harness:      r.spec.Wire,
		SessionAlias: r.alias(ev),
	})
	if err != nil {
		if adapter.IsUnmanaged(err) || ctx.Err() != nil {
			return r.unmanaged(err.Error())
		}
		return result{context: fmt.Sprintf("bicz: projection unavailable (%s); no state is assumed", protocol.CodeOf(err))}
	}
	rendered := adapter.RenderProjection(projection)
	// The cues come after the gated body: they are ledger data (see
	// adapter.RenderAccountability) and every active one is re-emitted here, which
	// is how a restarted or compacted session gets them back.
	return result{context: joinContext(rendered, r.accountability(ev.SessionID, &projection, true))}
}

// accountability renders the cues of confirmed penalties this session has not
// been given yet (all of them when all is set) and records that they were
// emitted. It adds no daemon call: the projection already rode along on the
// response the hook had in hand.
func (r *runner) accountability(sessionID string, p *protocol.Projection, all bool) string {
	if p == nil || p.PenaltySeq == 0 {
		return ""
	}
	calls, err := openStore(r.spec, r.stateDir)
	var after int64
	if err == nil && !all {
		after = calls.loadSeen(sessionID)
	}
	text := strings.TrimRight(adapter.RenderAccountability(*p, after), "\n")
	if text != "" && err == nil {
		calls.markSeen(sessionID, p.PenaltySeq)
	}
	return text
}

func joinContext(parts ...string) string {
	kept := parts[:0:0]
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n")
}

// write renders a result in the harness's response shape. A deny is the only
// path that sets a permission decision, and it can only set the deny value:
// there is deliberately no way to emit "allow" from this package.
func (r *runner) write(w io.Writer, res result) error {
	if res.deny == "" && res.context == "" {
		return nil
	}
	f := r.spec.Fields
	specific := map[string]any{f.OutEventName: r.eventName}
	if res.deny != "" {
		specific[f.OutPermissionDecision] = f.DenyValue
		specific[f.OutPermissionReason] = res.deny
	} else {
		specific[f.OutAdditionalContext] = res.context
	}
	encoded, err := json.Marshal(map[string]any{f.OutHookSpecific: specific})
	if err != nil {
		return fmt.Errorf("hook: encode response: %w", err)
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}
