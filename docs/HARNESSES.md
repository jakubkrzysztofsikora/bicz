# Harness support, in detail

Moved out of the top-level README, which has a shorter table. Per-adapter detail:
[`claude`](../adapters/claude/README.md), [`codex`](../adapters/codex/README.md),
[`omp`](../adapters/omp/README.md), [`opencode`](../adapters/opencode/README.md).

| Harness | Mechanism | Observed (adapter is in the call path) | Unobserved (named, not claimed) | Verified here |
| --- | --- | --- | --- | --- |
| Claude Code | `bicz hook` command hooks | SessionStart, PreToolUse, PostToolUse(+Failure), PreCompact | disabled hooks / `disableAllHooks`, hosted tools, subagent and MCP internals | hook CLI against a real daemon, **and** a live headless Claude Code 2.1.286 session (allow, deny, fail-open; [`RUNTIME_HOOKS.md`](RUNTIME_HOOKS.md)); `PreCompact` and mutating tools not exercised |
| Codex | `bicz hook` in `hooks.json` | SessionStart, UserPromptSubmit, PreToolUse, PostToolUse | disabled hooks, hosted tools, subagents/MCP, `apply_patch` | **real `codex-cli 0.162.1` (`codex exec`, mock model, temp home, hooks trusted via `hooks.state`)**: all four hooks ran with the field names bicz reads, `$bicz` answered with 0 model requests, a tool call reserved and settled, a bicz deny stopped the tool, **fail-open on hook error and timeout observed**; interactive TUI not exercised ([`RUNTIME_HOOKS.md`](RUNTIME_HOOKS.md) section 3) |
| Oh My Pi | extension | session_start, tool_call, tool_result, compaction | native tools without events, host bridges | `/bicz` slash command: **real `omp --mode rpc` 18.2.11** on a temp ledger (`status`, `reward`, no-correction `loss` returned native notify). Tool-call/result/compaction hooks: TS mock harness + real daemon; **no live OMP agent session with tool calls** |
| OpenCode V1 | plugin | tool.execute.before/after, failed-tool settlement (`message.part.updated`), `experimental.chat.system.transform` projection/receipt/cue, `command.execute.before` | `experimental.session.compacting`, `--pure`, TUI/ACP/desktop/web routes, tools not routed through the plugin | **real `opencode` 1.18.31** with a mock provider on a temp ledger: reserve then settle, failed tool settled as an ordinary failure, insufficient-credit deny reached the model as a tool error, receipt/projection/penalty cue reached the model, `/bicz status`/`reward`/no-correction `loss` wrote (or refused to write) before the model turn that relays them. The run found and fixed 4 plugin defects the TS mock harness could not see (half-closed socket under Bun 1.3.14, dead `additionalContext`, unsettled failures, `output.parts` reassignment); [`RUNTIME_OPENCODE.md`](RUNTIME_OPENCODE.md) |
| OpenCode V2 | plugin (`@opencode/plugin@2.0.26`) | `execute.before/after`, `context`, `compaction`, `/bicz` command | desktop/web/ACP routes, tools not routed through the plugin | **real engine `opencode v2.0.26` (`@opencode/cli@2.0.26`)** with a mock provider on a temp ledger: plugin loads; `/bicz` via the session command route (no model turn, no provider request); a real tool call reserved (hold 5000 mc) and settled; deny reached the engine as a tool error and the tool did not run; projection/receipt reached the model through `context`; the penalty cue through `compaction`; [`RUNTIME_OPENCODE.md`](RUNTIME_OPENCODE.md) |

## Release gate

```sh
bicz rehearsal --json                       # Phase A: mechanical, simulation-only
bicz release-gate                           # exits 1 unless a human also recorded Phase B evidence
```

Phase A proves the machinery with synthetic fixtures. Phase B ([`REHEARSAL.md`](REHEARSAL.md)) needs a human
operator, a dedicated test identity and a live model session, and cannot be derived from Phase A.
`bicz release-gate` rejects a missing or invalid Phase B record by design.
