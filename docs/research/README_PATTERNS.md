# README patterns from popular AI dev tools

Research note behind the bicz README rewrite. Method: `gh api repos/<repo>` for stars and
description, `gh api repos/<repo>/readme` for the raw README, `gh search repos` to sanity-check the
candidate set, and a fetch of the [Standard Readme spec](https://github.com/RichardLitt/standard-readme/blob/main/spec.md).
Data pulled **2026-10-11**; stars and READMEs move. Line numbers refer to the README as fetched that
day. "First fence" is the line of the first fenced code block, a rough proxy for "how far down is the
first command".

## 1. The sample (17 repos)

| Repo | Stars | Category | README lines | First fence |
| --- | ---: | --- | ---: | ---: |
| [anomalyco/opencode](https://github.com/anomalyco/opencode) | 212,559 | coding agent | 129 | 48 |
| [ollama/ollama](https://github.com/ollama/ollama) | 182,660 | local LLM runtime | 357 | 15 |
| [anthropics/claude-code](https://github.com/anthropics/claude-code) | 150,067 | coding agent | 72 | 22 |
| [langchain-ai/langchain](https://github.com/langchain-ai/langchain) | 147,565 | LLM framework | 80 | 31 |
| [ggml-org/llama.cpp](https://github.com/ggml-org/llama.cpp) | 130,768 | inference | 134 | 24 |
| [openai/codex](https://github.com/openai/codex) | 128,547 | coding agent | 81 | 18 |
| [google-gemini/gemini-cli](https://github.com/google-gemini/gemini-cli) | 107,277 | coding agent | 412 | 40 |
| [vllm-project/vllm](https://github.com/vllm-project/vllm) | 93,526 | inference | 111 | 68 |
| [modelcontextprotocol/servers](https://github.com/modelcontextprotocol/servers) | 91,128 | MCP tools | 170 | 61 |
| [OpenHands/OpenHands](https://github.com/OpenHands/OpenHands) | 90,520 | agent harness | 179 | 70 |
| [cline/cline](https://github.com/cline/cline) | 70,125 | coding agent | 235 | 50 |
| [BerriAI/litellm](https://github.com/BerriAI/litellm) | 60,910 | gateway, cost tracking, guardrails | 713 | not measured |
| [crewAIInc/crewAI](https://github.com/crewAIInc/crewAI) | 59,542 | agent framework | 741 | 115 |
| [aaif-goose/goose](https://github.com/aaif-goose/goose) (was block/goose) | 55,141 | agent harness | 63 | 37 |
| [Aider-AI/aider](https://github.com/Aider-AI/aider) | 49,457 | coding agent | 180 | 105 |
| [continuedev/continue](https://github.com/continuedev/continue) | 36,171 | coding agent (read-only now) | 63 | none |
| [promptfoo/promptfoo](https://github.com/promptfoo/promptfoo) | 25,878 | evals, red teaming | 98 | 27 |
| [guardrails-ai/guardrails](https://github.com/guardrails-ai/guardrails) | 7,504 | guardrails | 233 | 49 |

litellm was fetched but not read in depth (713 lines; its description line carries "cost tracking,
guardrails, load balancing"). `gh search repos "agent guardrails"` returns only small repos
(top hit 76 stars), so the guardrails category is represented by guardrails-ai and the
guardrail-adjacent promptfoo. The searches run here found no popular repo that gives the agent
credits and persistent consequences, so bicz has no direct README peer; the patterns below are
borrowed from neighbours.

## 2. Patterns, with evidence

### P1. A one-line value statement, short enough to be the repo description

- Standard Readme requires a short description under 120 characters that "must match GitHub's
  description" ([spec](https://github.com/RichardLitt/standard-readme/blob/main/spec.md)).
- The strongest sit at ~6-10 words: opencode "The open source coding agent." / ollama "Start building
  with open models." / llama.cpp "LLM inference in C/C++" / vLLM "Easy, fast, and cheap LLM serving for
  everyone" / aider "AI Pair Programming in Your Terminal" / langchain "The agent engineering platform."
- Weak: tools whose description is a paragraph (claude-code's description is ~230 characters;
  crewAI's is ~180). Readers skim past it.
- **Take:** one sentence, names the thing and the benefit, under 120 chars, identical in README,
  GitHub description and the packaging field.

### P2. Command before prose: first fence within ~25 lines

- Fastest to first command: ollama (line 15, no badges, no preamble), codex (18), claude-code (22),
  llama.cpp (24), promptfoo (27).
- Slowest: aider (105, but its screencast is at line 16, so the demo comes first and the fence later),
  crewAI (115, after a 741-line file's TOC and marketing), OpenHands (70).
- Install friction is one pasted line in the leaders: `curl -fsSL https://ollama.com/install.sh | sh`
  (ollama), `curl -fsSL https://claude.ai/install.sh | bash` (claude-code), `curl -fsSL
  https://chatgpt.com/codex/install.sh | sh` (codex), `curl -LsSf https://llama.app/install.sh | sh`
  (llama.cpp), goose's one-line CLI install, `python -m pip install aider-install && aider-install` (aider).
- **Take:** if there is no one-line install, say so up front and give the shortest honest path.
  Do not fake a `curl | sh` that does not exist.

### P3. Demo within the first screen, and the first "it worked" within ~3 commands

- claude-code puts `demo.gif` at line 11, before install (line 22). aider puts an animated
  screencast under the H1 (line 16). OpenHands puts a screenshot at line 29.
- Short loops to first value: llama.cpp is install then `llama cli -hf ggml-org/Qwen3.5-0.8B-GGUF`
  (2 commands to a running model). promptfoo is `npm install -g promptfoo`, `promptfoo init --example
  getting-started`, `promptfoo eval`, `promptfoo view` (4 commands, line 25-45). aider is install,
  `cd`, `aider --model ...`.
- **Take:** a pasted terminal transcript of real output is the cheapest "demo" a CLI tool can ship.
  No GIF needed if the transcript is real and short.

### P4. Audience routing above the fold

- codex lines 1-8 route three audiences in the first screen: terminal (this repo), IDE (link),
  desktop app, cloud agent (link). cline lists CLI, desktop, VS Code, JetBrains, SDK, each with its own
  install block. OpenHands has a nav row (Quickstart | Docs | Self-Hosting | ACP Agents | ...).
- **Take:** a one-row harness support table does this job for bicz: "Claude Code / Codex / OMP /
  OpenCode: here is your route and its honest status."

### P5. Badges: few, and only ones that carry information

- Zero badges: ollama, codex, cline, vLLM. Two: claude-code (Node version, npm version). Five to six:
  opencode (3), promptfoo (5), goose (5, including a Linux Foundation health score and packaging
  status), llama.cpp (6, mostly CI status per component).
- Overloaded: guardrails-ai has 11 badge lines before any text. Aider's badge row includes an install
  count ("6.8M installs") and star count, which is evidence rather than decoration.
- **Take:** badges must be true and verifiable. For an unpublished pre-release, only static, true
  badges (license, status). No CI, coverage or download badge until those exist.

### P6. "Why" as 3-6 bullets with concrete, checkable claims

- gemini-cli "Why Gemini CLI?" leads with numbers: "60 requests/min and 1,000 requests/day" free
  tier. promptfoo "Why Promptfoo?": "Private: LLM evals run 100% locally - your prompts never leave your
  machine". langchain "Why use LangChain?": 6 bullets, mostly generic adjectives (the weakest of the
  three).
- **Take:** each bullet states one mechanism and one consequence, and is checkable in the repo.
  Adjectives ("powerful", "seamless") are what to cut.

### P7. Section order: hook, value, install, first use, how it works, links out

- Short leaders follow almost exactly: claude-code (hook, demo, Get started, Plugins, Reporting Bugs),
  codex (hook, Quickstart, Docs), promptfoo (hook, Quick Start, What can you do, Why, Learn More,
  Contributing), llama.cpp (hook, Quick start, Description, Supported backends, Documentation).
- The 400-700 line files (gemini-cli 412, crewAI 741, litellm 713) bury install behind features or a
  TOC and repeat the docs site.
- Standard Readme order: Title, Banner, Badges, Short Description, Long Description, TOC, Security,
  Background, Install, Usage, Extra Sections, API, Contributing, License. Its own rule: TOC optional
  under 100 lines.
- **Take:** under ~130 lines, no TOC, deep material in `docs/` with links.

### P8. State limitations in callouts, near the thing they limit

- OpenHands puts a `[!WARNING]` directly on the no-sandbox install options: "the agent will have full
  access to your filesystem!", and carries a `status: beta` badge.
- modelcontextprotocol/servers opens (lines 5-9) with `[!IMPORTANT]` and `[!WARNING]`: "reference
  implementations... not production-ready solutions".
- continue puts a `Note` at line 19: the repository "is no longer actively maintained and is
  read-only". claude-code flags npm install as `[!NOTE] deprecated`.
- goose links "Known Issues" under "Need help?".
- **Take:** honesty up top reads as confidence, not weakness. These projects are among the most
  starred in the set and none hides the caveat. For bicz the caveat ("not a sandbox") is the product's
  most important sentence; it gets its own short section above install, not a footnote.

### P9. Voice: one line of personality, never at the cost of clarity

- opencode labels its `curl | bash` block `# YOLO`. goose ends with "a little goose humor". The rest
  stay flat.
- **Take:** humor in the hook and one or two captions; the install, usage and limits sections are
  literal.

### P10. Link out, do not duplicate

- ollama, codex, goose, claude-code push everything past quickstart to a docs site. promptfoo ends
  with "Learn More" links. gemini-cli's 412 lines duplicate docs and read slowly.
- **Take:** README under ~150 lines; every deep doc already in `docs/` is linked from one "Docs"
  section.

## 3. Top five patterns adopted for bicz

1. Hook that names a felt problem in two sentences, then a one-sentence value statement (P1, P7:
   claude-code, codex, promptfoo).
2. Transcript-as-demo with real captured output, within the first screen after install (P3: aider,
   claude-code, llama.cpp).
3. Install in the fewest honest commands, first fence within ~25 lines (P2: ollama, codex, llama.cpp).
4. A named, short "What it isn't" section above install (P8: OpenHands, MCP servers, continue).
5. A harness-routing table with honest per-row status, and everything deep linked out of `docs/`
   (P4, P10: codex, cline, goose).

## 4. Boilerplate skeleton for bicz

Rules the skeleton encodes: every claim traceable to a repo file or a command run; no badge that is
not true; no section above install except hook, value line, why, and limits; under ~150 lines.

```markdown
# bicz: installing obedience                                   <- title + 3-word joke, nothing else

<2 sentences: the felt problem. Concrete, dated by habit not by version.
 "Your agent said X. Y was false. It will do it again, because ...">

<1 paragraph: the pitch. "bicz makes it cost something." Persistent credits, role, lessons.
 Plain statement that it records and shapes incentives; does not enforce.>

<static badges only: license, status pre-release>

## What it is                                                  <- ONE sentence, names the parts
## Why it helps                                                <- 3 bullets, each mechanism + consequence
## What it isn't                                               <- short, blunt, above install
   not enforcement / not a sandbox / not auth / effect unmeasured (with link to the trial docs)
## Install                                                     <- first fence here
   shortest REAL path (clone + one build command); one command per line, comments only where needed;
   then the 4-5 setup commands; a "not published yet" note instead of a fake curl|sh
## 60-second demo                                              <- real captured output, labelled as such
   status -> loss -> (agent's next session sees it) -> reward
## Works with                                                  <- harness table: route + honest status
## How it works                                                <- 4-6 lines: ledger, supervisor, adapters, cue
## Docs                                                        <- link list into docs/ and root docs
## License                                                     <- one line, last
```
