package hooks

import "encoding/json"

// ClaudeSpec is the Claude Code command-hook adapter (docs/CONTRACTS.md §5.6).
//
// Claude Code runs the configured command per event, writes the event JSON on
// stdin (session_id, hook_event_name, tool_name, tool_input, tool_use_id,
// tool_response) and reads hookSpecificOutput from stdout:
//
//   - PreToolUse deny: permissionDecision "deny" + permissionDecisionReason.
//     On allow the adapter emits no permissionDecision at all, so Claude's own
//     permission prompts still run.
//   - SessionStart / PostToolUse / PostToolUseFailure: additionalContext.
//   - PreCompact: the projection is emitted as additionalContext, but whether
//     Claude accepts it for that event is not verified; the durable
//     post-compaction projection is SessionStart with source "compact", which
//     the installer's SessionStart entry (no matcher) also receives.
var ClaudeSpec = &Spec{
	ID:         "claude",
	Wire:       "claude-code",
	ConfigKind: "bicz.claude-config",
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
		{Name: "PreToolUse", Kind: KindPreToolUse, Matcher: "*"},
		{Name: "PostToolUse", Kind: KindPostToolUse, Matcher: "*"},
		{Name: "PostToolUseFailure", Kind: KindPostToolUseFailure, Matcher: "*"},
		{Name: "PreCompact", Kind: KindPreCompact},
		// /bicz: Claude Code delivers the typed command to this hook as JSON
		// (command_name, command_args) before the skill expands, so the arguments
		// never pass through a shell and no model is involved.
		{Name: "UserPromptExpansion", Kind: KindSlash, Matcher: "bicz", Timeout: slashHookTimeout, Slash: claudeSlash},
	},
}

// claudeSlash recognizes a typed /bicz: a UserPromptExpansion for the slash
// command named exactly "bicz". command_args is the raw text after it; prompt_id
// identifies this one submission; transcript_path is the session transcript
// Claude Code hands every hook event (observed live, docs/RUNTIME_HOOKS.md
// section 2), which the executor may use for inference (internal/infer).
func claudeSlash(fields map[string]json.RawMessage) (string, string, string, bool) {
	if jsonString(fields, "command_name") != "bicz" {
		return "", "", "", false
	}
	if kind := jsonString(fields, "expansion_type"); kind != "" && kind != "slash_command" {
		return "", "", "", false
	}
	return jsonString(fields, "command_args"), jsonString(fields, "prompt_id"), jsonString(fields, "transcript_path"), true
}
