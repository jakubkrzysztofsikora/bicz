package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// This file is the operator slash-command path of the Claude Code and Codex
// adapters. The harness hands the typed command to the hook as one JSON field
// (Claude Code: UserPromptExpansion.command_args; Codex: UserPromptSubmit.prompt),
// the hook runs it, and the result is returned as a block decision whose reason
// the harness shows to the operator. So:
//
//   - the typed text is JSON data end to end: it is never interpolated into a
//     shell command, a here-document, or a prompt, so no typed text can end a
//     delimiter or run a command;
//   - no model is involved: the harness blocks the prompt before any model
//     sees it, so no model transcribes, chooses, or retries anything;
//   - a harness that does not run the hook (hooks disabled, untrusted, or
//     removed) executes nothing at all: the installed skill is an inert notice.
//
// A block decision is the documented way to answer a command without a model
// turn; it does not mean the command was refused.

// slashHookTimeout is the hook timeout (seconds) the installer writes for slash
// events. A loss is a short local SQLite write; this leaves room for a busy
// ledger without letting a stuck hook stall the session.
const slashHookTimeout = 15

// slashDeadline bounds one slash execution inside the hook. It is deliberately
// longer than the ledger's 10 s SQLite busy timeout, so a locked ledger is
// refused by SQLite itself ("database is locked", the write never began) before
// this deadline fires, and shorter than slashHookTimeout, so the harness never
// kills the process mid-write.
const slashDeadline = 12 * time.Second

// SlashFunc runs one slash command to completion and returns its result text.
// It MUST honor ctx and MUST be truthful about outcome: the hook runs it
// synchronously and exits only after it returns, so nothing can commit after
// the result was reported. invocation is unique to the submission (empty when
// the harness supplies none).
type SlashFunc func(ctx context.Context, harness, arguments, invocation string) string

func jsonString(fields map[string]json.RawMessage, name string) string {
	var out string
	if raw, ok := fields[name]; ok {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// runSlash handles a KindSlash event. An event that is not a /bicz invocation
// produces no output, so the harness proceeds exactly as if no hook existed.
func runSlash(opts Options, spe EventSpec) error {
	raw, err := io.ReadAll(io.LimitReader(opts.Stdin, maxStdin))
	if err != nil {
		return fmt.Errorf("hook: read stdin: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("hook: event JSON on stdin is malformed: %w", err)
	}
	arguments, invocation, ok := spe.Slash(fields)
	if !ok {
		return nil
	}
	if opts.Slash == nil {
		return fmt.Errorf("hook: %s has no slash executor", spe.Name)
	}
	deadline := opts.Timeout
	if deadline <= 0 {
		deadline = slashDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	// Synchronous on purpose. The ledger write is one context-aware transaction
	// (BeginTx(ctx), rolled back when ctx ends before commit), so the executor
	// itself reports a cancelled or timed-out command; abandoning a goroutine
	// here would let the process exit, or the write commit, after an
	// "outcome unknown" answer.
	text := opts.Slash(ctx, opts.Spec.ID, arguments, invocation)
	text = strings.TrimSpace(text)
	// reason is what Claude Code shows for a blocked prompt; systemMessage is the
	// common output field both harnesses surface as a UI/event-stream message, so
	// the operator sees the result whichever surface renders which field.
	encoded, err := json.Marshal(map[string]any{"decision": "block", "reason": text, "systemMessage": text})
	if err != nil {
		return fmt.Errorf("hook: encode response: %w", err)
	}
	_, err = opts.Stdout.Write(append(encoded, '\n'))
	return err
}
