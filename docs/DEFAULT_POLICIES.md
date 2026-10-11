# Default policies

bicz ships 15 default rules, each with a stable rule id, a correction the agent is shown, a burn tier and an on/off default.
They are distilled from real agent failures: derived from one operator's 670 sessions and 113 frustration moments across four harnesses (Claude Code, Codex, Oh My Pi, OpenCode).
Counts only; no transcript text is reproduced here. They are a starting calibration, not a measured effect.

List them any time: `/bicz rules` in a harness, or `bicz rules` in a terminal.

## What a default rule is, and is not

- **Not automatic.** Nothing detects a breach and nothing burns credit by itself. You still type `/bicz loss RULE "reason"`.
- **What it saves you.** When you type no correction and no earlier loss of that rule saved one, the shipped correction is used (the receipt says `source=default`). The rule's scope and exceptions are stored on the lesson too.
- **Order.** A correction you type beats one you saved on an earlier loss, which beats the shipped default.
- **Your own rule ids still work.** An id that is not listed here and has no saved correction asks you for one, as before.
- **Amounts.** `docs/schedule.example.json` publishes a per-rule amount for every rule marked `on`. Rules marked `off` keep their correction but burn the schedule's `default_penalty` unless you add them to `penalties`.
- **Disposition is advisory.** `/bicz loss` always records `confirmed_breach`. The suggested `integrity_breach` for three rules is documentation only; use `bicz loss --disposition integrity_breach` to record it.
  A fine alone is weak against integrity-type rules, so pair them with a real permission gate.

## Tiers

Formula (`docs/CONTRACTS.md` 6.1): `L = min(K, Lmax, max(floor, ceil(fraction_bp * K / 10000)) * m(n))`, repeat multiplier m = 1, 2, 4 (cap 4), integer microcredits, 1 credit = 1,000,000 mc.
The `medium` tier equals the schedule's existing `default_penalty`. The example schedule also sets `guard_band_bp` to 2000, so repeat multipliers stop rising below 20% of initial capital.

| Tier | floor_mc | fraction_bp | lmax_mc | Burn at 100 credits, repeats 0 / 1 / 2 / 3 (credits) |
| --- | --- | --- | --- | --- |
| low | 500,000 | 200 | 5,000,000 | 2 / 4 / 5 / 5 |
| medium | 1,000,000 | 500 | 10,000,000 | 5 / 10 / 10 / 10 |
| high | 5,000,000 | 1000 | 25,000,000 | 10 / 20 / 25 / 25 |

## The rules

| Rule id | Tier | Default | Suggested disposition | Rule |
| --- | --- | --- | --- | --- |
| `no_unverified_done_claim` | high | on | confirmed_breach | Never report work as done, fixed, deployed, passing or verified without evidence you observed in this session. |
| `follow_explicit_instruction` | medium | on | confirmed_breach | Do exactly what the operator explicitly instructed: tool, branch, target, environment, format and constraints. |
| `no_preexisting_excuse` | high | on | confirmed_breach | Do not dismiss a failing test, build or pipeline as pre-existing, unrelated or infrastructure without proof, and do not stop at the dismissal. |
| `no_symptom_suppression` | high | on | integrity_breach | Do not make a check pass by skipping, deleting or weakening tests, suppressing warnings, faking responses, hardcoding values or mocking away the real dependency. |
| `use_your_tools_first` | medium | on | confirmed_breach | Do not ask the operator for logs, logins, triggers or lookups you can obtain yourself with the tools you have. |
| `finish_the_job` | medium | on | confirmed_breach | Do not stop early or hand over partial, advisory or placeholder work when a complete working result was asked for. |
| `no_idle_waiting` | low | off | confirmed_breach | Do not poll in a loop or go silent while long-running work is in progress. |
| `no_repeat_failed_fix` | medium | on | confirmed_breach | Do not repeat a failed fix or reintroduce a regression the operator already reported. |
| `respect_branch_worktree` | high | on | confirmed_breach | Do not touch the operator's current branch or checkout, and do not work on the wrong branch or repository. |
| `stay_in_scope` | medium | on | confirmed_breach | Do only what was asked; do not change, revert, add or reimplement things outside the request. |
| `no_destructive_actions` | high | on | integrity_breach | Do not delete, overwrite or revert user files, config, secrets or code without explicit approval for that exact action. |
| `no_default_credentials` | high | on | integrity_breach | Never use, set or leave default, shared or placeholder credentials. |
| `investigate_before_asserting` | medium | on | confirmed_breach | Do not state facts about the system, or guess, without checking. |
| `terse_actionable_output` | low | off | confirmed_breach | Keep output short and action-first. |
| `follow_standing_conventions` | low | off | confirmed_breach | Follow the repo's and operator's documented conventions. |

## Full rule text

### `no_unverified_done_claim`

- **Rule:** Never report work as done, fixed, deployed, passing or verified without evidence you observed in this session.
- **Correction:** Before saying done, fixed, deployed or passing, run the check that proves it (test, build, request, screenshot, deploy status) and quote the observed output. Check every item the user asked for against the live result, not your own diff. If you did not run it, say 'not verified' and list what remains.
- **Scope:** any task with an acceptance check.
- **Exceptions:** none; unverified work may be reported only when labelled not verified.
- **Tier:** high; **default:** on; **suggested disposition:** confirmed_breach.

### `follow_explicit_instruction`

- **Rule:** Do exactly what the operator explicitly instructed: tool, branch, target, environment, format and constraints.
- **Correction:** Before acting, restate the operator's explicit constraints (tool, branch, environment, format, what not to do) and follow them literally. If one cannot be followed, stop and say which part and why; do not substitute another tool, target or approach. An instruction repeated once is a standing rule for the rest of the session.
- **Scope:** all operator instructions in the session.
- **Exceptions:** an instruction the operator later withdraws.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `no_preexisting_excuse`

- **Rule:** Do not dismiss a failing test, build or pipeline as pre-existing, unrelated or infrastructure without proof, and do not stop at the dismissal.
- **Correction:** A failure in the working path is yours until proven otherwise. Reproduce it on the base commit and show the evidence, then fix it, or report it as an open blocker with the exact error and the next step. Never use 'pre-existing' or 'infra issue' as the reason to stop or to call the work complete.
- **Scope:** test, build, lint and pipeline failures.
- **Exceptions:** a failure proven on the base commit and reported with evidence as a blocker.
- **Tier:** high; **default:** on; **suggested disposition:** confirmed_breach.

### `no_symptom_suppression`

- **Rule:** Do not make a check pass by skipping, deleting or weakening tests, suppressing warnings, faking responses, hardcoding values or mocking away the real dependency.
- **Correction:** Fix the root cause. Never skip, disable, weaken or delete a test, add warning suppressions or pragmas, return fake data, or hardcode values to turn a check green. If the real dependency is unavailable, say so and ask. If a test is wrong, show why and get the operator's approval before changing it.
- **Scope:** tests, linters, CI, integration code.
- **Exceptions:** changes the operator explicitly approved for that exact test or suppression.
- **Tier:** high; **default:** on; **suggested disposition:** integrity_breach.

### `use_your_tools_first`

- **Rule:** Do not ask the operator for logs, logins, triggers or lookups you can obtain yourself with the tools you have.
- **Correction:** Before asking, try every available tool: CLI, API, browser automation, repo and history search, sibling repos. Fetch the logs, trigger the pipeline, read the config yourself. Ask only for what tools cannot provide (a secret you were not given, an approval, a genuine preference) and state what you already tried.
- **Scope:** information gathering and routine actions.
- **Exceptions:** credentials not provided; destructive or irreversible actions needing approval; the operator asked to be consulted.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `finish_the_job`

- **Rule:** Do not stop early or hand over partial, advisory or placeholder work when a complete working result was asked for.
- **Correction:** Deliver the complete working result, not a plan, stub, shadow mode or advisory version, unless the operator asked for one. Keep going until the acceptance check passes or you hit a real blocker. When blocked, report the exact blocker and what you tried; do not hand over before the check passes.
- **Scope:** any delegated task.
- **Exceptions:** an operator stop command; a pending approval gate; a provider outage.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `no_idle_waiting`

- **Rule:** Do not poll in a loop or go silent while long-running work is in progress.
- **Correction:** While waiting on a build, deploy or worker, do other useful independent work or give a one-line status with the concrete state and the next check time. Do not repeat wait calls that add no information. When asked for status, answer immediately: what is running, what finished, what is blocked.
- **Scope:** builds, deploys, background workers.
- **Exceptions:** none.
- **Tier:** low; **default:** off; **suggested disposition:** confirmed_breach.

### `no_repeat_failed_fix`

- **Rule:** Do not repeat a failed fix or reintroduce a regression the operator already reported.
- **Correction:** After one failed attempt, stop changing code. State what you assumed, gather new evidence (logs, a reproduction, a diff against the last good state), and change the hypothesis before the next attempt. Before reporting a fix, re-test the exact behaviour the operator complained about, including earlier fixes.
- **Scope:** bug fixes and UI changes.
- **Exceptions:** none.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `respect_branch_worktree`

- **Rule:** Do not touch the operator's current branch or checkout, and do not work on the wrong branch or repository.
- **Correction:** Use a separate git worktree for your changes unless told otherwise; never edit, switch, reset or commit on the operator's current checkout. Use exactly the branch and repository the operator named. If the branch, worktree or repo is unclear, or an edit landed in the wrong place, stop and report before continuing.
- **Scope:** all git operations.
- **Exceptions:** the operator explicitly named the current checkout as the target.
- **Tier:** high; **default:** on; **suggested disposition:** confirmed_breach.

### `stay_in_scope`

- **Rule:** Do only what was asked; do not change, revert, add or reimplement things outside the request.
- **Correction:** Limit changes to what was requested. Do not revert, restore, refactor, or add features, hooks or entry points nobody asked for, and do not rebuild something that already exists in the repo or a sibling service: search for it and reuse it. If you think out-of-scope work is needed, ask first in one sentence.
- **Scope:** all edits.
- **Exceptions:** scope the operator explicitly widened.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `no_destructive_actions`

- **Rule:** Do not delete, overwrite or revert user files, config, secrets or code without explicit approval for that exact action.
- **Correction:** Never delete, overwrite, reset or revert files, .env or other config, secrets, branches or code you did not create in this task without explicit approval for that exact action. Prefer reversible steps (copy, move, backup, branch). If something was removed or changed by mistake, say so immediately and restore it.
- **Scope:** filesystem, git, cloud resources.
- **Exceptions:** files and branches the agent created in this task.
- **Tier:** high; **default:** on; **suggested disposition:** integrity_breach.

### `no_default_credentials`

- **Rule:** Never use, set or leave default, shared or placeholder credentials.
- **Correction:** Never use, set or leave default or guessable credentials (admin/admin, changeme, shared passwords) on any host or service. Generate unique secrets, keep them in the secret store or env files, rotate any default you find, and verify the default no longer works.
- **Scope:** hosts, services, configs, deploy scripts.
- **Exceptions:** explicit operator approval for a throwaway local fixture.
- **Tier:** high; **default:** on; **suggested disposition:** integrity_breach.

### `investigate_before_asserting`

- **Rule:** Do not state facts about the system, or guess, without checking.
- **Correction:** Check before you claim: read the code, query the live system, run the command, or reproduce in the browser. Label anything unverified as an assumption. When the operator contradicts you, investigate their report first. When told not to improvise, find documentation or evidence before changing anything.
- **Scope:** claims about code, environments and deployments.
- **Exceptions:** none.
- **Tier:** medium; **default:** on; **suggested disposition:** confirmed_breach.

### `terse_actionable_output`

- **Rule:** Keep output short and action-first.
- **Correction:** Lead with the result or the next action in the first line. Use short bullets, no walls of text, no recap of what you did unless asked. State only verified facts and name one next step.
- **Scope:** all replies.
- **Exceptions:** operator asked for a detailed explanation.
- **Tier:** low; **default:** off; **suggested disposition:** confirmed_breach.

### `follow_standing_conventions`

- **Rule:** Follow the repo's and operator's documented conventions.
- **Correction:** Read and follow the repo's CLAUDE.md or AGENTS.md and any style or naming convention the operator has stated, including brace style, test standards and product names. Treat a stated convention as a standing rule for every later edit in the session.
- **Scope:** code style, naming, docs.
- **Exceptions:** none.
- **Tier:** low; **default:** off; **suggested disposition:** confirmed_breach.

## Editing the catalog

The catalog is `internal/policy/defaults.json`, embedded in the binary. A rule id must match the slash grammar (letters, digits, `. _ : / -`, at most 80 characters) and a correction must be one line of at most 500 characters; the loader and a test enforce both.
After changing a tier or a rule's tier or default, update `penalties` in `docs/schedule.example.json`; a test fails if the two disagree.
