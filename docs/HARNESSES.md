# Harness support, in detail

Moved out of the top-level README, which has a shorter table. Per-adapter detail:
[`claude`](../adapters/claude/README.md), [`codex`](../adapters/codex/README.md),
[`omp`](../adapters/omp/README.md), [`opencode`](../adapters/opencode/README.md).

| Harness | Mechanism | Observed (adapter is in the call path) | Unobserved (named, not claimed) | Verified here |
| --- | --- | --- | --- | --- |
| Claude Code | `bicz hook` command hooks | SessionStart, PreToolUse, PostToolUse(+Failure), PreCompact | disabled hooks / `disableAllHooks`, hosted tools, subagent and MCP internals | hook CLI against a real daemon, **and** a live headless Claude Code 2.1.286 session (allow, deny, fail-open; [`RUNTIME_HOOKS.md`](RUNTIME_HOOKS.md)); `PreCompact` and mutating tools not exercised |
| Codex | `bicz hook` in `hooks.json` | SessionStart, PreToolUse, PostToolUse | disabled hooks, hosted tools, subagents/MCP, **fail-open on hook error/timeout** | hook CLI against a real daemon; real Codex 0.162.1 parses the generated `hooks.json` but marks it untrusted; **no live Codex hook run**, field names UNVERIFIED at runtime |
| Oh My Pi | extension | session_start, tool_call, tool_result, compaction | native tools without events, host bridges | `/bicz` slash command: **real `omp --mode rpc` 18.2.11** on a temp ledger (`status`, `reward`, no-correction `loss` returned native notify). Tool-call/result/compaction hooks: TS mock harness + real daemon; **no live OMP agent session with tool calls** |
| OpenCode V1 | plugin | tool.execute.before/after, session events | tools not routed through the plugin | TS mock harness + real daemon; **not** a real OpenCode |
| OpenCode V2 | plugin (`@opencode/plugin@2.0.26`) | `execute.before/after`, `context`, `compaction` | tools not routed through the plugin | typechecked against the real package, loaded by the real `Host.load`, real daemon; **ABI verified, V2 engine absent (not a live OpenCode)**; [`RUNTIME_OPENCODE.md`](RUNTIME_OPENCODE.md) |

## Release gate

```sh
bicz rehearsal --json                       # Phase A: mechanical, simulation-only
bicz release-gate                           # exits 1 unless a human also recorded Phase B evidence
```

Phase A proves the machinery with synthetic fixtures. Phase B ([`REHEARSAL.md`](REHEARSAL.md)) needs a human
operator, a dedicated test identity and a live model session, and cannot be derived from Phase A.
`bicz release-gate` rejects a missing or invalid Phase B record by design.
