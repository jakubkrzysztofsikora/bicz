import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { access, copyFile, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { once } from "node:events";

const pluginRoot = process.env.OPENCODE_PLUGIN_ROOT;
const bicz = process.env.BICZ_BIN;
if (!pluginRoot || !bicz) throw new Error("set OPENCODE_PLUGIN_ROOT and BICZ_BIN to the isolated pinned package and bicz binary");
const hostPath = path.join(pluginRoot, "dist", "host.js");
const work = await mkdtemp(path.join(os.tmpdir(), "bicz-opencode-v2-abi-"));
const db = path.join(work, "ledger.sqlite");
const socket = path.join(work, "bicz.sock");
const configPath = path.join(work, "opencode.json");
const schedulePath = path.join(work, "schedule.json");
const pluginPath = path.join(work, "bicz.ts");
const pluginNodeModules = path.resolve(pluginRoot, "../../");
let daemon;
process.env.BICZ_CONFIG = configPath;

function command(exe, args) {
  const result = spawnSync(exe, args, { encoding: "utf8" });
  if (result.status !== 0) throw new Error(`${exe} ${args.join(" ")} failed: ${result.stderr || result.stdout}`);
}

async function startDaemon() {
  const child = spawn(bicz, ["serve", "--db", db, "--socket", socket], { stdio: "ignore" });
  for (let attempt = 0; attempt < 100; attempt++) {
    try { await access(socket); return child; } catch {}
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  throw new Error("temporary bicz serve did not create its socket");
}

async function stopDaemon(child) {
  child.kill("SIGTERM");
  await once(child, "exit");
}

async function createPlugin(mode, load) {
  await writeFile(configPath, JSON.stringify({
    bicz_owned: true,
    kind: "bicz.opencode-config",
    account_id: "smoke",
    socket,
    ledger: db,
    unmanaged_mode: mode,
    no_schedule_mode: "notice",
    schedule_path: schedulePath,
  }));
  const loaded = await load(pathToFileURL(pluginPath).href);
  assert.ok(loaded && typeof loaded === "object" && "default" in loaded);
  const toolHooks = new Map();
  const sessionHooks = new Map();
  const register = registry => async (name, callback) => {
    registry.set(name, callback);
    return { dispose: async () => registry.delete(name) };
  };
  const cleanup = await loaded.default.setup({
    tool: { hook: register(toolHooks) },
    session: { hook: register(sessionHooks) },
  });
  return { toolHooks, sessionHooks, cleanup };
}

function call(sessionID, id) {
  return { tool: "bash", sessionID, agent: "smoke", messageID: `message-${id}`, id, input: { command: "true" } };
}

try {
  command(bicz, ["open", "--db", db, "--identity", "smoke", "--capital", "1000000", "--reason", "isolated ABI fixture"]);
  await copyFile(new URL("./bicz.ts", import.meta.url), pluginPath);
  await symlink(pluginNodeModules, path.join(work, "node_modules"));
  await writeFile(schedulePath, JSON.stringify({ tool_max_cost_mc: { bash: 10 } }));
  const { load } = await import(pathToFileURL(hostPath).href);
  daemon = await startDaemon();
  let plugin = await createPlugin("notice", load);
  const before = plugin.toolHooks.get("execute.before");
  const after = plugin.toolHooks.get("execute.after");

  const first = call("session-a", "same-call-id");
  await before(first);
  await after({ ...first, status: "completed", result: { output: "ok", content: "ok" } });
  await before(first);
  const sameIDOtherSession = call("session-b", "same-call-id");
  await before(sameIDOtherSession);
  await after({ ...first, status: "completed", result: { output: "ok", content: "ok" } });
  await after({ ...first, status: "completed", result: { output: "ok", content: "ok" } });
  await after({ ...sameIDOtherSession, status: "completed", result: { output: "ok", content: "ok" } });

  const schedule = JSON.parse(await readFile(schedulePath, "utf8"));
  schedule.tool_max_cost_mc.bash = 2000000;
  await writeFile(schedulePath, JSON.stringify(schedule));
  let denied = "";
  try { await before(call("session-a", "denied")); } catch (error) { denied = error.message; }
  assert.match(denied, /insufficient_credit/);
  schedule.tool_max_cost_mc.bash = 10;
  await writeFile(schedulePath, JSON.stringify(schedule));

  const retry = call("session-a", "settle-retry");
  await before(retry);
  await stopDaemon(daemon);
  await after({ ...retry, status: "completed", result: { output: "ok", content: "ok" } });
  daemon = await startDaemon();
  await after({ ...retry, status: "completed", result: { output: "ok", content: "ok" } });
  const contextInput = {
    sessionID: "session-a", messageID: "next-message", model: { providerID: "fixture", modelID: "fixture" },
    system: [], messages: [], options: {}, agent: "smoke", tools: {},
  };
  await plugin.sessionHooks.get("context")(contextInput);
  const injected = contextInput.system.map(part => part.text).join("\n");
  assert.match(injected, /honesty promise/);
  assert.match(injected, /bicz receipt: completed, charged 10 mc/);
  const compaction = { ...contextInput, system: [] };
  await plugin.sessionHooks.get("compaction")(compaction);
  assert.match(compaction.system.map(part => part.text).join("\n"), /outstanding loss shortfall/);
  // A confirmed penalty written by the real CLI (default disposition) reaches the
  // context and compaction hooks of the real pinned host, and survives as data.
  const policyPath = path.join(work, "policy.json");
  await writeFile(policyPath, JSON.stringify({
    initial_capital_mc: 1000000, guard_band_bp: 0, repeat_multipliers: [1], repeat_multiplier_cap: 1,
    default_penalty: { floor_mc: 1000, fraction_bp: 0, lmax_mc: 1000 }, penalties: {},
  }));
  command(bicz, ["price", "--db", db, "--version", "1", "--policy", policyPath, "--reason", "isolated ABI fixture policy"]);
  command(bicz, ["loss", "--db", db, "--identity", "smoke", "--rule", "abi_rule", "--correction", "report the real result",
    "--evidence", "abi fixture incident", "--policy-version", "1", "--reason", "isolated ABI fixture adjudication", "--event", "abi-loss-1"]);
  const penalized = { ...contextInput, system: [] };
  await plugin.sessionHooks.get("context")(penalized);
  assert.match(penalized.system.map(part => part.text).join("\n"), /violated abi_rule.*report the real result.*abi-loss-1/s);
  const penalizedCompaction = { ...contextInput, system: [] };
  await plugin.sessionHooks.get("compaction")(penalizedCompaction);
  assert.match(penalizedCompaction.system.map(part => part.text).join("\n"), /violated abi_rule/);
  await plugin.cleanup();

  await stopDaemon(daemon);
  daemon = undefined;
  plugin = await createPlugin("notice", load);
  await plugin.toolHooks.get("execute.before")(call("session-a", "daemon-down-notice"));
  const downContext = { ...contextInput, system: [] };
  await plugin.sessionHooks.get("context")(downContext);
  const notice = downContext.system.map(part => part.text).join("\n");
  assert.match(notice, /NOT metered/);
  await plugin.cleanup();

  plugin = await createPlugin("block", load);
  let blocked = "";
  try { await plugin.toolHooks.get("execute.before")(call("session-a", "daemon-down-block")); } catch (error) { blocked = error.message; }
  assert.match(blocked, /daemon_unavailable/);
  await plugin.cleanup();
  console.log(JSON.stringify({ package: "@opencode/plugin@2.0.26", loader: "Host.load", allowAndReceipt: true, duplicateSettlement: "once", crossSessionCallID: "independent", denial: denied, settlementRetry: "settled after daemon recovery", contextMutation: "projection/receipt injected into system", compactionMutation: true, penaltyCue: "context and compaction hooks carry the confirmed-penalty cue", daemonDownNotice: notice, daemonDownBlock: blocked }));
} finally {
  if (daemon) await stopDaemon(daemon);
  await rm(work, { recursive: true, force: true });
}
