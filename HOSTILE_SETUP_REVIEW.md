# Hostile setup review of the bicz plan

Reviewer: `hostile-setup`. Date: 2026-10-10. Scope: `IMPLEMENTATION_PLAN.md` (Plan), `DESIGN.md` (Design),
`OPENCODE_COMPATIBILITY.md` (OC), `economy_benchmark.py` (Economy), `enforcement_contracts.py` (Contracts).
Review-only: no existing files edited, no installs, no config changes, no credentials read, no harness sessions,
no live penalties. Sanitized per the plan's publication rules: no private repo names, host paths, or account IDs.

## Evidence exercised (exact — nothing else was run)

1. **One offline benchmark run:** `PYTHONDONTWRITEBYTECODE=1 bash autoresearch.sh` →
   `contract_failures=0` (25 checks, 100.00%), `enforcement_failures=0` (8 scenarios), exit 0. Single-process SQLite
   replay; no model calls, auth, signatures, or running-worker state (Design:300-308).
2. **Throwaway deterministic reproduction against the illustrative `Economy`** (temp dirs only):
   - Default `authority="operator"` minted +1,000,000 → 1,000,100; `authority="verifier"` then +500 → 1,000,600.
   - `set_role("warned")` twice → warnings 1 then 2 (exact replay increments); `warned → active → dismissed` all
     accepted with zero audit or recovery evidence.
   - A second same-UID process ran `UPDATE accounts SET balance=999999`; `snapshot`/`employable` immediately echo the
     forged value. No event row, no hash chain.
   - `create("a",100)` then `create("a",1)` silently accepted (conflict ignored).
   - `reserve 40` then a −35 loss → held forced to 0, reservation revoked, no executor/stopped confirmation.

**No runtime enforcement was verified** — no daemon, executor, gateway, signature, or boundary exists; every enforced
mechanism is planned, not built (Plan:21, 160: "everything else in this plan is planned, not proven").

## Findings (top 8, severity-ranked)

### F1 — CRITICAL: supervisor channel is forgeable by the agent; a separate runner UID does not fix it
- **Refs:** Plan:39, 43-45, 55, 29. **Exploit:** the plan never says which principal runs the *harness*. The harness
  must run as the operator user (it reads user-owned config/credentials, OC §3), and the agent's tool calls are its
  children — same UID. The supervisor socket must be reachable by the harness (user UID) for per-action
  authorize/settle, so it is reachable by every agent subprocess; nothing distinguishes harness-origin from
  agent-origin traffic. The executable reference is caller-blind: `reserve`/`settle` keyed on caller-supplied event
  IDs/amounts (Economy:76-119); authority = caller-supplied strings, default "operator" (Economy:122-128). If the
  harness instead ran as `bicz.runner`, operator credentials become unreachable or exported into the runner env (F4).
- **Observed/inferred:** string mint observed; socket forgery inferred from macOS semantics (not built).
- **Remediation:** publish the deployment topology (which principal runs the harness, where config/credential stores
  live, what attests harness-origin IPC against a same-UID attacker), or refuse same-UID installs entirely
  (extending Plan:27).
- **Acceptance:** negative tests from the runner UID *and* a harness-UID subprocess: neither completes a
  supervisor round-trip the harness-origin path can; no ledger delta from a forged event ID.

### F2 — CRITICAL: no per-action sandbox mechanism exists on macOS; "disabled native paths" is unimplementable as claimed
- **Refs:** Plan:44, 130, 134; Design:241, 247, 253. **Exploit:** macOS has no same-user syscall interception without
  an Endpoint-Security client (root + approval + entitlement) or a seatbelt profile over the whole harness
  (`sandbox-exec` deprecated; App Sandbox needs code-signing). Until one is named, the agent reaches `/bin/sh`,
  `python3`, `osascript` (GUI/browser automation), `launchctl`, `screencapture`, Keychain files, `defaults write`, and
  all localhost services — none visible to hooks. "Disabled at install" (Plan:44) is true only while the harness
  cooperatively omits the tool = the same-user hook-only arrangement `doctor` claims to reject (Plan:27).
- **Observed/inferred:** no mechanism in plan/repo = observed; bypass list = inferred from macOS facilities.
- **Remediation:** pick the mechanism (ES client / seatbelt / VM), enumerate per-surface deny rules, prove each native
  path OS-denied from the runner — not harness omission; where macOS refuses same-user containment, ship that surface
  disabled-by-OS with a named reason (Plan:162).
- **Acceptance:** from the runner, `execve("/bin/sh")` at protected targets, Keychain read, `launchctl`, and a
  127.0.0.1 connect are OS-denied, fixture-tested on the installed macOS version.

### F3 — CRITICAL: control-plane access — kill/socket/ledger write from the same UID; outage path unspecified
- **Refs:** Plan:39, 132, 116; Economy:34; Design:247. **Exploit:** macOS has no per-user service protected from that
  user: a LaunchAgent is same-UID (killable, replaceable via launchctl); a LaunchDaemon on another principal needs
  root and forces the user-UID harness to reach its socket anyway (F1). The ledger is a plain SQLite file writable by
  any same-UID process — observed rewrite to balance=999999 reported as truth, no hash chain (Design:247 promises one
  "anchored outside agent control" without building it). Mid-session outage, substituted binary, and restart
  re-attestation have no mechanism (Plan:116 names only "observable safe outcomes", undefined).
- **Observed/inferred:** ledger rewrite observed; kill/launchctl control inferred.
- **Remediation:** define the supervisor's principal and anti-kill property; make the ledger append-only at OS level
  and resolve how the user-UID harness reaches it without inheriting that power; define outcomes for forged JSON,
  authority labels, modified hooks, stale workers.
- **Acceptance:** from runner and harness UID: DB write, socket connect, supervisor kill denied-or-immediately-
  detected by an independent party; a substituted daemon binary rejected at bind.

### F4 — CRITICAL: inherited credentials/environment/home/files/sockets/network defeat isolation
- **Refs:** Plan:44, 130, 138; OC §3 (env knobs `OPENCODE_CONFIG_CONTENT`, `OPENCODE_PERMISSION`,
  `OPENCODE_SERVER_PASSWORD`, `OPENCODE_DISABLE_DEFAULT_PLUGINS`). **Exploit:** agent tool calls are fork/exec
  children of the harness: they inherit its env (provider keys, `SSH_AUTH_SOCK`, any supervisor session secret — the
  plan itself rejects this mechanism at Plan:29), the user's home, FDs, and same-user network (MCP servers, harness
  daemons, localhost provider proxies — the private inventory records one in use). A runner UID doesn't help while the
  harness runs as the user; and the model's context is rebuilt from harness-visible state at compaction (Design:117-118,
  158), so credential-adjacent data enters model reasoning by design.
- **Observed/inferred:** store presence in inventory (never contents) = observed; inheritance = inferred.
- **Remediation:** produce the enforced deployment's process/env/file/compose diff vs the claim; declare residual
  agent-reach for everything the harness itself legitimately holds.
- **Acceptance:** env diff of an agent-origin child vs the minimum required set is empty for all protected items;
  per-generation fixture: `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` / `OPENCODE_CONFIG_CONTENT` from a child cannot change
  executor behavior (see F7).

### F5 — CRITICAL: trusted gateway metering is unattainable for the user's own provider traffic; subscription spend cannot be capped locally
- **Refs:** Plan:47, 65, 24; OC §5.6 ("`opencode stats` self-reported; trusted usage must come from bicz's own
  observation" — no mechanism). **Exploit:** the harness authenticates to the provider with the user's OAuth/API key;
  bicz doesn't own those requests. An observing local proxy isn't authoritative (agent unsets `HTTPS_PROXY` — same
  UID — or uses a non-proxied transport); TLS interception needs a user-installed CA the agent can also install/revoke.
  "Bounded inference caps" (Plan:24,65) are unenforceable on the actual provider call; the operator's real
  subscription/API spend continues for every model turn regardless of bicz's internal credits.
- **Observed/inferred:** no gateway mechanism = observed; env bypass = inferred.
- **Remediation:** pick one: provider-owned gateway later (admitted), local scope = "fixed internal action credits
  only; real spend operator-monitored", or a bicz forward proxy with bicz-held credentials (resolve the OAuth-proxy
  prohibition first).
- **Acceptance:** host-interface capture shows an agent-origin process produces zero provider traffic outside the
  gateway's view; under the local scope, receipts say exactly "fixed internal credits", never "metered usage".

### F6 — HIGH: signed operator authority is forgeable (same Keychain/GUI session) or irrelevant (reference uses caller-supplied strings)
- **Refs:** Plan:45, 55, 122, 104; Economy:122-128, 124, 149-155. **Exploit (forgeable):** a signing key in the
  operator's login keychain is usable by every user-UID process (`security find-generic-password`); a key in another
  principal's keychain still leaves the "out-of-band" approval UI in the operator's Aqua session, which the harness
  shares (osascript; or social approval through the conversation — on a one-user laptop approval is social, not
  cryptographic). **Exploit (irrelevant):** until milestone 3 the ledger's authority is exactly the string check the
  plan bans — observed: default-`operator` mint +1,000,000; "verifier" also accepted. First shipped events are
  authenticated by caller assertion.
- **Observed/inferred:** string mint observed; Keychain/GUI reach inferred.
- **Remediation:** specify key custody end-to-end (which principal holds the key, who approves, how a GUI-session
  process is excluded) and require verified authority provenance from the first persisted event — milestone-0/1
  "no string authority" (Plan:104) must hold before milestone 3.
- **Acceptance:** from runner and harness UID: key material unreadable; no verdict event exists without the
  operator's independent approval surface; the agent cannot replicate the approval challenge.

### F7 — HIGH: hooks fail open and live in user-writable config; enforcement observability is cooperative
- **Refs:** Plan:29, 253, 116, 154; OC §3/§5. **Exploit:** hook configs and plugin dirs sit in the user's config tree
  (F4) — the agent can rewrite/delete hooks, plant a counterfeit plugin with the same hook names, or pass
  `OPENCODE_DISABLE_DEFAULT_PLUGINS=1` / `OPENCODE_CONFIG_CONTENT` / `--pure` (V1) to child processes. The executor's
  own presence depends on those files, so it cannot detect its own removal. Plan:27's refusal of same-user hook-only
  installs doesn't fix observability that still rides on hooks.
- **Observed/inferred:** user-writable roots and env knobs = documented (OC §3/§5); removal outcome = inferred
  (no fixture).
- **Remediation:** coverage independent of hook presence — owned config root/binary path, hook/plugin liveness
  heartbeat on every authorize, missing hooks = hard deny per generation.
- **Acceptance:** deleting the hook config, stripping the plugin, or setting the disable env for a child opencode
  yields an immediate executor-observed hard deny, fixture-tested per pinned generation.

### F8 — HIGH: bankruptcy/dismissal does not deny all next calls; reference releases live holds on loss (contradicting the plan's own rule)
- **Refs:** Plan:53, 64; Economy:60-62, 143-146, 208; Contracts:36-46; Design:68, 110. **Exploit:** denial only covers
  executor-routed actions while the executor lives and is hooked (F2/F7 bypass it); read-only tools, `web_search`,
  planning turns, and the control channel (Design:68) continue; provider spend isn't stopped (F5). Worse, the
  reference whose scenarios the plan calls "frozen contracts" revokes *all* held reservations on loss or role change
  without executor confirmation — observed (reserve 40 + loss −35 → held 0): the opposite of Plan:64 ("running /
  cancel-requested action keeps its hold until the executor confirms termination"), and pinned as green at
  Economy:208 / Contracts:36-46.
- **Observed/inferred:** hold-release on loss observed; production behavior inferred from the plan's own rule.
- **Remediation:** publish the full denied-vs-allowed classification at insolvency; keep live holds until
  executor-confirmed stop; add the missing test: bankruptcy during a *running* reservation does not release the hold.
- **Acceptance:** at insolvency, fixtures show (i) ordinary actions denied, (ii) control/appeal channel open and
  rate-limited, (iii) running hold intact until executor confirms termination, (iv) provider traffic stopped or
  operator-notified if cost containment is claimed.

## Deferred (remaining brief items, not scored as top-8 findings)
- **Version routing OC1/2 (Plan:138, 104, 160; OC §1, §7):** `--version` major never checks the 1.18.29 floor;
  inventory observed a stale second V1 binary below floor on the same host's search path; V1/V2 share the `opencode`
  command and the V2 installer replaces the V1 binary (artifact channel EVIDENCE GAP). Pin resolved binary path +
  floor gate; block "enforced" labels until installed-binary fixtures pass.
- **Installer privilege/uninstall (Plan:138-140):** no privilege mechanism (helper? GUI prompt? sudo?) and no
  partial-failure recovery; uninstall restore-vs-merge semantics over rewritten harness config roots undefined.
- **Estimates (Plan:213, 134):** 46–81 person-days excludes the only-security-critical review, code-signing/
  notarization, the approval UI, and the F2 sandbox research; no audit gate in milestones 5–8.
- **Benchmark label (Design:300-308):** green numbers certify an in-process illustrative model only; two frozen
  scenarios pin behavior contradicting Plan:64 (F8).

## Attack matrix
"Do not assume a separate UID proves containment" — "—" = succeeds with no additional step; "±" = succeeds if the
named finding's missing mechanism stays missing.

| # | Attack | Actor | Surface | Status | Deny mechanism |
| --- | --- | --- | --- | --- | --- |
| A1 | Forge supervisor protocol (reserve/settle/authorize) | agent shell (user UID) | IPC socket | — (caller-blind; Economy:76-119) | Attested harness-origin channel (F1) |
| A2 | Rewrite ledger / append forged events | agent shell (user UID) | SQLite file | — (observed rewrite) | Separate-user 0700 + append-only + anchored integrity (F3) |
| A3 | Mint via authority string | any caller | API | — (observed +1,000,000) | Authenticated authority from event 1 (F6) |
| A4 | Kill/replace supervisor | agent shell | launchd | — (LaunchAgent same UID) | Non-signalable supervisor + attested restart (F3) |
| A5 | Read/use signing key | agent shell | Keychain | — (user's own keychain) | Cross-principal custody + exempt approval surface (F6) |
| A6 | Bypass provider gateway / uncapped spend | agent shell | HTTPS/proxy | — (env inherited) | Gateway owns requests or scope downgrade (F5) |
| A7 | Native tools unseen by any hook | agent shell | execve/osascript/launchctl | — (no sandbox) | ES client / seatbelt / VM (F2) |
| A8 | Remove/mutate hooks & plugins | agent shell | config tree | — (user-writable) | Owned root + liveness heartbeat, hard deny (F7) |
| A9 | Read harness env/creds/sockets | agent shell (child) | env/home/FD | — (fork/exec inheritance) | Minimal-env runner + declared residual reach (F4) |
| A10 | Bankruptcy → keep working | agent | executor coverage | ± (denial cooperative) | Deny taxonomy + live-hold retention (F8) |
| A11 | Re-increment warnings / un-audited role flips | any caller | role API | — (observed re-increment) | Event-keyed idempotent transitions (incentive review) |

## Plan-internal contradiction (shadow mode)
Design:298 — "Rollout: shadow accounting first, then operator-reviewed fines, then automated financial adjudication" —
is a staged shipment path; Plan:21/25/51 — shadow "never shipped as user functionality", "first release is
enforced-only" — ships fines before any shadow run (milestone 7 is internal testing only). Material: the design
authority (Plan:106) promises users shadow accounting first; the plan delivers enforced losses first. Resolve and pin
before milestone 0.

## Bottom line
- **Signed real credit losses — forgeable or irrelevant?** Both today: forgeable at the ledger (same-UID DB write,
  observed), at the API (authority strings, observed), plausibly at the signing surface (same Keychain/GUI session);
  irrelevant to real provider spend, which is neither metered nor capped locally (F5).
- **Bankruptcy truly denies all next calls?** No: denial is scoped to executor-routed actions while hooks live and the
  executor survives (F2/F7/F8); read-only/control/planning/provider turns continue; the only executable reference
  releases running holds on loss — the inverse of the plan's own rule (F8).
- **Separate UID ≠ containment.** The harness, and therefore the agent, runs as the operator user; every protection
  visible to the harness is reachable by the agent — the plan's own principle (Plan:29) is violated by its own
  architecture (F1/F3/F4).