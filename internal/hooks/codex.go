package hooks

import (
	"encoding/json"
	"strings"
)

// CodexSpec is the Codex command-hook adapter (docs/CONTRACTS.md §5.6).
//
// VERIFIED at runtime against codex-cli 0.162.1 (docs/RUNTIME_HOOKS.md section 3): a real
// `codex exec` ran SessionStart, UserPromptSubmit, PreToolUse and PostToolUse hooks and every
// field below matched the payload it sent (session_id, turn_id, cwd, hook_event_name, model,
// permission_mode, transcript_path (null), tool_name, tool_use_id, tool_input, tool_response
// (a bare string, not an object), prompt, source) and the hookSpecificOutput deny and
// additionalContext shapes it accepted. If a different Codex version renames a field, change it
// HERE, in this one table, and nowhere else.
//
// Observed Codex behaviors that bound what this adapter can see (coverage table; never
// described as enforced): hooks fail open on hook error or timeout (observed), hosted tools do
// not run hooks, and Codex has no PostToolUseFailure event: PostToolUse also fires for a shell
// command that exited non-zero, but tool_response is only the output text (rust-v0.162.1
// ExecCommandToolOutput::post_tool_use_response; the exit code lives in the model-facing header,
// not in the hook payload) and the input schema has no exit or error field. Failure is not
// unambiguous, so bicz never infers it from text: it settles as completed (same charge). Codex
// 0.162.1 does list PreCompact and PostCompact events, but their
// output schema carries no additionalContext, so bicz cannot restore a projection through them;
// compaction restoration relies on SessionStart with source "compact" (not exercised live). Only
// SessionStart, PreToolUse, PostToolUse and UserPromptSubmit are installed.
var CodexSpec = &Spec{
	ID:         "codex",
	Wire:       "codex",
	ConfigKind: "bicz.codex-config",
	Fields: Fields{
		SessionID:    "session_id",
		EventName:    "hook_event_name",
		ToolName:     "tool_name",
		ToolInput:    "tool_input",
		ToolUseID:    "tool_use_id",
		ToolResponse: "tool_response",

		OutHookSpecific:       "hookSpecificOutput",
		OutEventName:          "hookEventName",
		OutPermissionDecision: "permissionDecision",
		OutPermissionReason:   "permissionDecisionReason",
		OutAdditionalContext:  "additionalContext",
		DenyValue:             "deny",
	},
	Events: []EventSpec{
		{Name: "SessionStart", Kind: KindSessionStart},
		{Name: "PreToolUse", Kind: KindPreToolUse},
		{Name: "PostToolUse", Kind: KindPostToolUse},
		// $bicz: Codex has no user-defined slash commands; a skill is invoked by
		// typing $bicz, and this hook receives that prompt as JSON before the
		// model does. Codex ignores a matcher on this event.
		{Name: "UserPromptSubmit", Kind: KindSlash, Timeout: slashHookTimeout, Slash: codexSlash},
	},
}

// codexSlash recognizes a prompt that starts with the token $bicz. Everything
// after the token is the raw slash text; anything else (including $biczx or
// a mention later in a sentence) is an ordinary prompt and is left alone.
// transcript_path is forwarded when Codex supplies one; it was observed as
// null on the 0.162.1 exec path (docs/RUNTIME_HOOKS.md section 3), in which
// case it is "" and the executor discloses that to the infer model honestly.
func codexSlash(fields map[string]json.RawMessage) (string, string, string, bool) {
	prompt := strings.TrimLeft(jsonString(fields, "prompt"), " \t\r\n")
	const token = "$bicz"
	if !strings.HasPrefix(prompt, token) {
		return "", "", "", false
	}
	rest := prompt[len(token):]
	if rest != "" && !strings.ContainsRune(" \t\r\n", rune(rest[0])) {
		return "", "", "", false
	}
	return rest, jsonString(fields, "session_id") + ":" + jsonString(fields, "turn_id"), jsonString(fields, "transcript_path"), true
}
