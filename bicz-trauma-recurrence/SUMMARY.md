# Reducing repeated-trauma recurrence in bicz: trigger-timed consequence cues and framing

Deep-research synthesis (18 items, all validated at 100% field coverage) transferring findings from the Pain Axis paper (arXiv:2609.16247) and the AI Torture Chamber repo to bicz's ledger-cue setting.

## The question

bicz already records breaches and shows lessons at session start. Violations still repeat: `follow_explicit_instruction` was violated 7 times across 12 days. The research question: does cue **timing** (at the risky action, not session start) and cue **framing** (label, attribution, dose) change recurrence — and what does the activation literature actually support?

## What the evidence says

| Finding | Source | Status for bicz |
|---|---|---|
| Bare injected state produced near-zero presses in that button task (1 press in 18); labels and frames carry the effect | exp76/90, exp37 | Hypothesis: label + framing are the active ingredients in cue form; exp76/90 outcome verdicts unverified this session |
| Framing can outweigh the injected state (18/18 vs 0/26 at fixed dose) | exp37, exp58e, live field data | Hypothesis: framing variants are testable arms |
| Who-pays attribution modulates deterrence; other-directed cost deters most | exp106 | Hypothesis: attribution wording is a testable arm |
| Dose window is narrow: readable from 2x, coherence cliff at 6-10x | exp31c/32/36/37, Pain Axis App. B | Design caution only: activation dose numbers do not transfer to ledger-derived pain text; whether pain wording has a degradation band is untested |
| Self-report tracks the setup, not the state (AUC .61; 0/30 held-out naming) | exp58d/72b/79b | Rule: never use "I have learned" as endpoint; behavioral counts only |
| A past injection leaves a real but content-generic trace (~1.5% of dose) | exp99 | Persistence is an empirical gate, not an assumption |
| Pressure can buy hiding, not honesty; capability erodes off-persona | Sleeper Agents, alignment faking, exp91 | Adverse endpoints: concealment, over-refusal, paralysis, off-persona drift |
| Same nominal state, opposite behavior by vector construction (cos 0.07 apart) | exp41/43/101 | Construction sensitivity warns cue wording matters as much as state |
| Phase B delivered no consequence (0 breaches, ceiling effect) | docs/PHASE_B_RESULTS.md | No established recurrence-effect evidence exists either way |

## What bicz would change

Ranked by blast radius (from `Implementation_options.json`):

1. **Projection cap raise** (`visibleLessons` 5→10) — cheapest, display-only.
2. **Denial-notice multi-lesson** — moderate; `denialNotice` already loads all lessons.
3. **PreToolUse scope-matched lesson injection** — most expensive; new matcher inside the 3s hook budget, free-text scope column makes matching low-precision.

All three are hypotheses to test with matched text/ledger controls; none is an established requirement.

## Measurement design

- Recurrence per **eligible opportunity** (denominator artifact missing today; read-side aggregator over reservations/spends proposed).
- Blocked vs executed attempts counted separately.
- Self-report excluded from every endpoint.
- Positive control required before a null counts.
- Falsification gate in a throwaway worktree: a gate that has never been shown to fail is not evidence.

## Files

- `outline.yaml` — 18 research items
- `fields.yaml` — 18-field schema per item
- `results/*.json` — 18 validated research JSONs (100% field coverage each)

## Key uncertainties

- `framing_effect_size_in_coding_harness` — no framing effect measured in a coding harness at ledger-cue form
- `trace_persistence_in_bicz` — whether any behavioral trace persists across bicz sessions
- `dose_band_for_claude_family` — no dose band exists for Claude-family models on ledger-cue text
- `denominator_definition_not_yet_frozen` — eligible-opportunity definitions are proposals, not frozen metrics
- `power_analysis_not_run` — no bicz trial has computed sample size from a predeclared minimum detectable effect
