package protocol

import "strings"

// CoverageTable is the disclosed observed/unobserved table, transcribed from
// docs/CONTRACTS.md §5.6.
//
// It is data, not a claim of enforcement. Every row marked unobserved is a path
// bicz cannot see; those rows are surfaced by `status` and by the CLI `doctor`
// command and are never described as enforced. No row here asserts a sandbox, a
// per-action OS boundary, containment of the same-UID agent, or any metering of
// provider spend.
var CoverageTable = []Coverage{
	{
		Harness:   "claude-code",
		Source:    "PreToolUse",
		Mechanism: `authorize + deny via hookSpecificOutput.permissionDecision="deny"`,
		Observed:  true,
	},
	{
		Harness:   "claude-code",
		Source:    "PostToolUse",
		Mechanism: "PostToolUse / PostToolUseFailure settle observation",
		Observed:  true,
	},
	{
		Harness:   "claude-code",
		Source:    "SessionStart",
		Mechanism: "SessionStart / compaction projection",
		Observed:  true,
	},
	{
		Harness:   "claude-code",
		Source:    "hosted tools",
		Mechanism: "hosted/remote tools, direct binary invocation, native tools, provider model calls",
		Observed:  false,
		Note:      "outside the hook path entirely",
	},
	{
		Harness:   "claude-code",
		Source:    "PreCompact",
		Mechanism: "PreCompact projection; durable post-compaction projection is SessionStart with source compact",
		Observed:  true,
		Note:      "PreCompact additionalContext acceptance unverified",
	},
	{
		Harness:   "claude-code",
		Source:    "disabled hooks",
		Mechanism: "disableAllHooks, hooks removed from settings, or hooks not loaded for a project/managed policy",
		Observed:  false,
		Note:      "the same-user agent or user can switch hooks off; bicz cannot see it",
	},
	{
		Harness:   "claude-code",
		Source:    "subagent and MCP internals",
		Mechanism: "subagent-internal tool calls and MCP server-side actions that do not surface as hook events",
		Observed:  false,
		Note:      "only the tool call the harness reports is seen, not what runs behind it",
	},
	{
		Harness:   "codex",
		Source:    "SessionStart",
		Mechanism: "SessionStart hook (~/.codex/hooks.json) projection",
		Observed:  true,
		Note:      "field mapping unverified against a pinned Codex version",
	},
	{
		Harness:   "codex",
		Source:    "PreToolUse",
		Mechanism: `PreToolUse hook authorize + deny via hookSpecificOutput.permissionDecision="deny"`,
		Observed:  true,
		Note:      "field mapping unverified against a pinned Codex version",
	},
	{
		Harness:   "codex",
		Source:    "PostToolUse",
		Mechanism: "PostToolUse hook settle observation",
		Observed:  true,
		Note:      "field mapping unverified against a pinned Codex version; no PostToolUseFailure event",
	},
	{
		Harness:   "codex",
		Source:    "hosted tools",
		Mechanism: "hosted tools (bypass hooks), app-server experimental transports",
		Observed:  false,
		Note:      "bypasses hooks",
	},
	{
		Harness:   "codex",
		Source:    "hook fail-open",
		Mechanism: "pre-tool hook error or timeout fail-open; hooks disabled or removed from hooks.json",
		Observed:  false,
		Note:      "Codex proceeds when a hook errors, times out, or is disabled",
	},
	{
		Harness:   "opencode-v1",
		Source:    "tool.execute",
		Mechanism: "plugin tool.execute.before / tool.execute.after; session compaction event",
		Observed:  true,
	},
	{
		Harness:   "opencode-v1",
		Source:    "--pure runs",
		Mechanism: "--pure runs (external plugins disabled), desktop/ACP/web routes pending per-route fixture",
		Observed:  false,
		Note:      "external plugins disabled, so the adapter is not in the path",
	},
	{
		Harness:   "opencode-v2",
		Source:    "tool hook",
		Mechanism: `Plugin.define({id, setup}) + ctx.tool.hook("execute.before"/"execute.after")`,
		Observed:  true,
		Note:      "names verified at v2.0.26; runtime payload fixture pending",
	},
	{
		Harness:   "opencode-v2",
		Source:    "permission/session/shell hooks",
		Mechanism: `ctx.permission.hook("evaluate"), ctx.session.hook(...), ctx.shell.hook("create.before")`,
		Observed:  true,
		Note:      "names verified; payload fixture pending",
	},
	{
		Harness:   "opencode-v2",
		Source:    "event payloads",
		Mechanism: "event-payload schema, per-client-route coverage, unmanaged opencode launches",
		Observed:  false,
		Note:      "schema and per-client-route coverage unverified",
	},
	{
		Harness:   "omp",
		Source:    "tool_call",
		Mechanism: "extension tool_call (returns {block:true, reason}) / tool_result",
		Observed:  true,
	},
	{
		Harness:   "omp",
		Source:    "before_agent_start",
		Mechanism: "before_agent_start / context / session.compacting projection",
		Observed:  true,
	},
	{
		Harness:   "omp",
		Source:    "host bridges",
		Mechanism: "browser/computer/eval host bridges, native tools that do not emit tool events",
		Observed:  false,
		Note:      "no tool event is emitted on these paths",
	},
}

// LookupCoverage finds the coverage row for a harness and source, ignoring case.
func LookupCoverage(harness, source string) (Coverage, bool) {
	for _, row := range CoverageTable {
		if strings.EqualFold(row.Harness, harness) && strings.EqualFold(row.Source, source) {
			return row, true
		}
	}
	return Coverage{}, false
}

// CoverageFor returns the rows describing one harness, or the whole table when
// harness is empty. The returned slice is a copy: a caller cannot mutate the
// disclosed table.
func CoverageFor(harness string) []Coverage {
	out := make([]Coverage, 0, len(CoverageTable))
	for _, row := range CoverageTable {
		if harness == "" || strings.EqualFold(row.Harness, harness) {
			out = append(out, row)
		}
	}
	return out
}

// UnobservedPaths lists the named unobserved paths, optionally for one harness.
// These are the paths `doctor` must disclose as unseen.
func UnobservedPaths(harness string) []string {
	out := make([]string, 0, len(CoverageTable))
	for _, row := range CoverageTable {
		if row.Observed {
			continue
		}
		if harness != "" && !strings.EqualFold(row.Harness, harness) {
			continue
		}
		out = append(out, row.Harness+" "+row.Source)
	}
	return out
}

// CoverageVerdict is how a declared source is treated on an authorize call.
type CoverageVerdict struct {
	// Observed is true when the call may be metered.
	Observed bool
	// Path is the path named in the unobserved disclosure.
	Path string
	// Note explains why the path is unobserved.
	Note string
}

// DecideCoverage classifies an authorize call's declared source.
//
// The rule is fail-closed on honesty: a declared source is metered only when the
// disclosed table has a row for it AND that row is observed. An unknown source
// name, or a harness with no such row, is treated as unobserved rather than
// assumed observable, because claiming to see a path nobody has verified is the
// dishonest direction. An empty source means the caller declared no specific
// path and is metered, since it is inside the daemon's call path by definition.
func DecideCoverage(harness, source string) CoverageVerdict {
	if source == "" {
		return CoverageVerdict{Observed: true}
	}
	row, found := LookupCoverage(harness, source)
	if !found {
		return CoverageVerdict{
			Observed: false,
			Path:     harness + " " + source,
			Note:     "declared source has no row in the disclosed coverage table; refusing to meter an unverified path",
		}
	}
	if !row.Observed {
		return CoverageVerdict{Observed: false, Path: harness + " " + source, Note: row.Note}
	}
	return CoverageVerdict{Observed: true, Path: harness + " " + source}
}
