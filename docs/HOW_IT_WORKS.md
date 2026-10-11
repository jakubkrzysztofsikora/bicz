# What bicz is, in parts

Moved out of the top-level README, which has a shorter "How it works". Design rationale:
[`DESIGN.md`](../DESIGN.md), [`CONTRACTS.md`](CONTRACTS.md), [`PRESSURE_MECHANICS.md`](../PRESSURE_MECHANICS.md).

A local, same-user **action-credit ledger and supervisor** for coding-agent harnesses. An operator
(the human at the terminal) funds an identity with internal credits. Harness adapters ask the
supervisor to reserve credit before a tool call and settle it afterwards. The operator can record
rewards and losses. Every change is an append-only, hash-chained ledger event.

- **Ledger** (SQLite, hash chain, optional external anchor): balance, holds, role, lessons, price
  schedules. Money is integer microcredits (1 credit = 1,000,000 microcredits). Internal credits
  are **not** provider billing and never equal cash.
- **Supervisor** (`bicz serve`): the one writer. Adapters talk to it over a local socket.
- **Operator CLI**: `open`, `reward`, `loss`, `appeal`, `review`, `reinstate`, `price`, `status`,
  `verify`, `anchor`, `doctor`, `lessons`.
- **Automatic accountability cue**: `bicz loss` (disposition `confirmed_breach` by default;
  `--disposition integrity_breach|administrative`) writes the burn, the lesson and the penalty in one
  transaction. A confirmed penalty prints a deterministic cue on the operator receipt and reaches every
  adapter before its next tool call or turn and again after restart or compaction: "Your recorded
  action violated `<rule>`… Take responsibility: `<correction>`… `<N>` credits were deducted…".
  Honest failures, budget exhaustion and `--disposition administrative` debits never produce it. It
  is a behavior-specific cue; bicz cannot cause or verify shame, and its behavioral effect is unmeasured
  (CONTRACTS.md §6.4).
- **Adapters**: Claude Code and Codex via command hooks (`bicz hook`), Oh My Pi via an extension,
  OpenCode V1/V2 via plugins. Each is installed by `bicz install --harness ...`.
