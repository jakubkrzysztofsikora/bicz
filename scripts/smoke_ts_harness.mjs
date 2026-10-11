// Mock-harness driver for the shipped TypeScript adapters, against a REAL bicz
// daemon. It imports adapters/omp/extension.ts and adapters/opencode/v1/bicz.ts
// (Node native type stripping) and calls the handlers the way a harness would.
//
// This is NOT a real Oh My Pi or OpenCode runtime: the harness objects are
// minimal fakes written from the adapters' own call shapes. OpenCode V2 is not
// exercised at all (its runtime fixture is still pending and it needs
// @opencode/plugin, which is not installed).
//
// usage: node scripts/smoke_ts_harness.mjs --bicz BIN --ledger DB --root REPO --tmp DIR --sock SOCKET
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";

const arg = (name) => {
  const i = process.argv.indexOf(`--${name}`);
  if (i < 0 || !process.argv[i + 1]) throw new Error(`missing --${name}`);
  return process.argv[i + 1];
};
const bicz = arg("bicz"), ledger = arg("ledger"), root = arg("root"), tmp = arg("tmp"), sock = arg("sock");
const schedule = join(root, "docs", "schedule.example.json");
const balance = () => JSON.parse(execFileSync(bicz, ["status", "--db", ledger, "--identity", "tsmock"], { encoding: "utf8" }));

let passed = 0;
const ok = (name) => { passed++; console.log(`  ok   ${name}`); };

// --- OMP -------------------------------------------------------------------------
{
  const config = join(tmp, "omp-smoke.json");
  writeFileSync(config, JSON.stringify({
    bicz_owned: true, kind: "bicz.omp-config", version: 1, account_id: "tsmock", socket: sock,
    unmanaged_mode: "notice", no_schedule_mode: "notice", schedule_path: schedule, policy_version: "1",
  }));
  process.env.BICZ_CONFIG = config;
  const mod = await import(pathToFileURL(join(root, "adapters/omp/extension.ts")).href);
  const handlers = new Map();
  const commands = new Map();
  mod.default({ on: (event, fn) => handlers.set(event, fn), registerCommand: (name, spec) => commands.set(name, spec) });
  assert.match(commands.get("bicz")?.description ?? "", /status \| rules \| loss/, "the /bicz command is registered and advertises rules");
  assert.equal(typeof commands.get("bicz")?.handler, "function");
  ok("OMP registers /bicz with a handler that advertises rules");
  const ctx = { cwd: "/tmp/mock", ui: { setStatus() {} } };

  const allowed = await handlers.get("tool_call")({ toolName: "Bash", toolCallId: "omp-1", input: { command: "ls" } }, ctx);
  assert.equal(allowed, undefined, "allow returns nothing");
  assert.equal(balance().held, 50000);
  ok("OMP tool_call reserves the published Bash price (50000 mc)");

  const receipt = await handlers.get("tool_result")({ toolCallId: "omp-1", toolName: "Bash", input: { command: "ls" }, content: "x", isError: false });
  assert.match(receipt.additionalContext, /charged 50000 mc/);
  assert.deepEqual([balance().balance, balance().held], [150000, 0]);
  ok("OMP tool_result settles the reservation with a receipt");

  const again = await handlers.get("tool_result")({ toolCallId: "omp-1", toolName: "Bash", input: {}, content: "x", isError: false });
  assert.equal(again, undefined);
  assert.equal(balance().balance, 150000);
  ok("OMP duplicate tool_result does not charge twice");

  const ctxBlock = await handlers.get("tool_call")({ toolName: "WebFetch", toolCallId: "omp-2", input: {} }, ctx);
  assert.equal(ctxBlock, undefined);
  const deny = await handlers.get("tool_call")({ toolName: "Task", toolCallId: "omp-3", input: {} }, ctx);
  assert.equal(deny?.block, true, "second 100000 mc reservation exceeds the 100000 mc left");
  assert.match(deny.reason, /insufficient_credit/);
  ok("OMP tool_call blocks on insufficient credit");
  await handlers.get("tool_result")({ toolCallId: "omp-2", toolName: "WebFetch", input: {}, content: "x", isError: false });
}

// --- OpenCode V1 ------------------------------------------------------------------
{
  const config = join(tmp, "opencode-smoke.json");
  writeFileSync(config, JSON.stringify({
    bicz_owned: true, kind: "bicz.opencode-config", version: 1, account_id: "tsmock", socket: sock,
    unmanaged_mode: "notice", no_schedule_mode: "notice", schedule_path: schedule, policy_version: "1",
  }));
  process.env.BICZ_CONFIG = config;
  const mod = await import(pathToFileURL(join(root, "adapters/opencode/v1/bicz.ts")).href);
  const plugin = await mod.default({});
  const before = plugin["tool.execute.before"], after = plugin["tool.execute.after"], transform = plugin["experimental.chat.system.transform"], onEvent = plugin.event;
  const startBalance = balance().balance;
  const systemText = async (sessionID) => { const o = { system: [] }; await transform({ sessionID }, o); return o.system.join("\n"); };

  // Real OpenCode 1.18.31 shapes (docs/RUNTIME_OPENCODE.md): before {tool, sessionID, callID} + {args}; after {tool, sessionID, callID, args} + {title, output, metadata}.
  await before({ tool: "Read", sessionID: "oc1-s", callID: "oc1-1" }, { args: { path: "a" } });
  assert.equal(balance().held, 5000);
  ok("OpenCode V1 tool.execute.before reserves the published Read price (5000 mc)");

  await after({ tool: "Read", sessionID: "oc1-s", callID: "oc1-1", args: { path: "a" } }, { title: "", output: "ok", metadata: {} });
  assert.equal(balance().balance, startBalance - 5000);
  assert.equal(balance().held, 0);
  assert.match(await systemText("oc1-s"), /bicz receipt: completed, charged 5000 mc, fine 0/);
  assert.doesNotMatch(await systemText("oc1-s"), /bicz receipt/, "a receipt is delivered once");
  ok("OpenCode V1 tool.execute.after settles it; the receipt reaches the model through system.transform once");

  await before({ tool: "Read", sessionID: "oc1-s", callID: "oc1-f" }, { args: {} });
  assert.equal(balance().held, 5000);
  await onEvent({ event: { type: "message.part.updated", properties: { part: { type: "tool", callID: "oc1-f", sessionID: "oc1-s", state: { status: "error" } } } } });
  assert.equal(balance().held, 0);
  assert.equal(balance().balance, startBalance - 10000);
  assert.match(await systemText("oc1-s"), /bicz receipt: ordinary_failure, charged 5000 mc, fine 0/);
  ok("OpenCode V1 settles a failed tool (V1 never calls tool.execute.after for it) as an ordinary failure");

  await assert.rejects(before({ tool: "Task", sessionID: "oc1-s", callID: "oc1-2" }, { args: {} }), /insufficient_credit/);
  ok("OpenCode V1 tool.execute.before denies (throws) on insufficient credit");
}

// --- Accountability: a confirmed penalty written by the real `bicz loss` CLI ------------
// Default disposition (no flag) is confirmed_breach; the cue must reach each adapter on
// its next tool poll exactly once per penalty, and an explicit administrative debit never.
const loss = (event, ...extra) => execFileSync(bicz, ["loss", "--db", ledger, "--identity", "tsmock",
  "--rule", "ts_rule", "--correction", "report the real result", "--evidence", `ts incident ${event}`,
  "--policy-version", "1", "--reason", "ts smoke adjudication", "--amount", "1000", "--event", event, ...extra], { encoding: "utf8" });
{
  process.env.BICZ_CONFIG = join(tmp, "omp-smoke.json");
  const mod = await import(pathToFileURL(join(root, "adapters/omp/extension.ts")).href);
  const handlers = new Map();
  mod.default({ on: (event, fn) => handlers.set(event, fn), registerCommand() {} });
  const ctx = { cwd: "/tmp/mock", ui: { setStatus() {} } };
  const poll = async (id) => {
    const out = await handlers.get("tool_call")({ toolName: "Read", toolCallId: id, input: {} }, ctx);
    await handlers.get("tool_result")({ toolCallId: id, toolName: "Read", input: {}, content: "x", isError: false });
    return out;
  };
  assert.equal(await poll("omp-p0"), undefined);

  assert.match(loss("ts-admin", "--disposition", "administrative"), /ACCOUNTABILITY none disposition=administrative/);
  assert.equal(await poll("omp-p1"), undefined, "administrative debit emits no cue");
  ok("OMP: an explicit administrative debit emits no cue");

  assert.match(loss("ts-loss-1"), /ACCOUNTABILITY issued/);
  const first = await poll("omp-p2");
  assert.match(first.additionalContext, /violated ts_rule.*report the real result.*1000 mc.*ts-loss-1/s);
  assert.equal(await poll("omp-p3"), undefined, "delivered once");
  ok("OMP tool_call delivers the new penalty cue once");

  loss("ts-loss-2");
  const second = await poll("omp-p4");
  assert.match(second.additionalContext, /ts-loss-2/);
  assert.doesNotMatch(second.additionalContext, /ts-loss-1/);
  ok("OMP: a repeated penalty of the same rule is delivered again");

  const turn = await handlers.get("before_agent_start")();
  assert.match(turn.message.content, /ts-loss-1/);
  assert.match(turn.message.content, /ts-loss-2/);
  ok("OMP before_agent_start re-presents every active penalty");
}
{
  process.env.BICZ_CONFIG = join(tmp, "opencode-smoke.json");
  const mod = await import(pathToFileURL(join(root, "adapters/opencode/v1/bicz.ts")).href);
  const plugin = await mod.default({});
  const transform = plugin["experimental.chat.system.transform"];
  const systemText = async (sessionID) => { const o = { system: [] }; await transform({ sessionID }, o); return o.system.join("\n"); };
  const first = await systemText("oc1-p");
  assert.match(first, /ts-loss-1/);
  assert.match(first, /ts-loss-2/);
  loss("ts-loss-3");
  const next = await systemText("oc1-p");
  assert.match(next, /ts-loss-3/);
  assert.match(next, /ts-loss-1/, "every active cue rides each request, so a restarted or compacted session gets it back");
  ok("OpenCode V1 system.transform presents every active penalty cue on each model request");
}
console.log(`  ${passed} TS mock-harness checks passed`);
