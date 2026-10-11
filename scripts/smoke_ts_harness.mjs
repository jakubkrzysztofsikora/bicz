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
  mod.default({ on: (event, fn) => handlers.set(event, fn) });
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
  const before = plugin["tool.execute.before"], after = plugin["tool.execute.after"];
  const startBalance = balance().balance;

  const out = {};
  await before({ tool: "Read", id: "oc1-1", input: { path: "a" } }, out);
  assert.equal(out.additionalContext, undefined);
  assert.equal(balance().held, 5000);
  ok("OpenCode V1 tool.execute.before reserves the published Read price (5000 mc)");

  const res = {};
  await after({ id: "oc1-1", input: { path: "a" }, output: "ok" }, res);
  assert.equal(balance().balance, startBalance - 5000);
  assert.equal(balance().held, 0);
  ok("OpenCode V1 tool.execute.after settles it");

  await assert.rejects(before({ tool: "Task", id: "oc1-2", input: {} }, {}), /insufficient_credit/);
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
  mod.default({ on: (event, fn) => handlers.set(event, fn) });
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
  const before = plugin["tool.execute.before"], after = plugin["tool.execute.after"];
  const out = {};
  await before({ tool: "Read", id: "oc1-p1", input: { path: "a" } }, out);
  assert.match(out.additionalContext, /ts-loss-1/);
  assert.match(out.additionalContext, /ts-loss-2/);
  await after({ id: "oc1-p1", input: { path: "a" }, output: "ok" }, {});
  const quiet = {};
  await before({ tool: "Read", id: "oc1-p2", input: { path: "a" } }, quiet);
  assert.equal(quiet.additionalContext, undefined, "delivered once");
  await after({ id: "oc1-p2", input: { path: "a" }, output: "ok" }, {});
  loss("ts-loss-3");
  const third = {};
  await before({ tool: "Read", id: "oc1-p3", input: { path: "a" } }, third);
  assert.match(third.additionalContext, /ts-loss-3/);
  assert.doesNotMatch(third.additionalContext, /ts-loss-1/);
  await after({ id: "oc1-p3", input: { path: "a" }, output: "ok" }, {});
  ok("OpenCode V1 tool.execute.before delivers each new penalty cue once");
}
console.log(`  ${passed} TS mock-harness checks passed`);
