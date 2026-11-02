// Drives src/core.ts through `native dev --core --script` (the SDK's node
// host: update, effects and a virtual clock, no renderer) and checks the
// model and the commands it issues. Run from app/: node --test test/core.test.mjs
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

const appDir = new URL("..", import.meta.url).pathname;

// run dispatches msgs (objects; strings in a `$bytes` field are byte payloads)
// and returns every model after a dispatch and every command line.
function run(msgs) {
  const dir = mkdtempSync(join(tmpdir(), "compack-core-"));
  const script = join(dir, "script.ndjson");
  writeFileSync(script, msgs.map((m) => JSON.stringify(m)).join("\n") + "\n");
  const res = spawnSync("native", ["dev", "--core", "--script", script], { cwd: appDir, encoding: "utf8" });
  rmSync(dir, { recursive: true });
  assert.equal(res.status, 0, res.stderr + res.stdout);
  const models = [];
  const cmds = [];
  for (const line of res.stdout.split("\n")) {
    if (line.startsWith("model ")) models.push(JSON.parse(line.slice(6)));
    else if (line.startsWith("cmd ")) cmds.push(line.slice(4));
  }
  return { models, cmds, last: models[models.length - 1] };
}

const b = (text) => ({ $bytes: text });
const text = (bytes) => bytes.$bytes;
const line = (event) => ({ kind: "pack_line", line: b(JSON.stringify(event)) });
const linux = { kind: "probe_exit", code: 0, output: b("Linux\n/opt/compack/assets/compack\n") };
const typeInput = (path) => ({ kind: "edit_input", edit: { kind: "insert_text", text: b(path) } });

// The JSON value of `field=` in a command line (more text may follow it).
function field(cmd, name) {
  const start = cmd.indexOf(name + "=") + name.length + 1;
  let depth = 0;
  let inString = false;
  for (let i = start; i < cmd.length; i++) {
    const c = cmd[i];
    if (inString) {
      if (c === "\\") i++;
      else if (c === '"') inString = false;
    } else if (c === '"') inString = true;
    else if (c === "[" || c === "{") depth++;
    else if ((c === "]" || c === "}") && --depth === 0) return JSON.parse(cmd.slice(start, i + 1));
  }
  throw new Error(`no ${name} in ${cmd}`);
}

// The argv of the last pack spawn, decoded.
function packArgv(cmds) {
  const spawn = cmds.filter((c) => c.startsWith('spawn key="pack"')).pop();
  assert.ok(spawn, "no pack spawn");
  return field(spawn, "argv").map(text);
}

test("packs with the chosen options and reads compack's events", () => {
  const { models, cmds, last } = run([
    linux,
    typeInput("/packs/My Pack.zip"),
    { kind: "preset_smallest" },
    { kind: "pack" },
    line({ event: "start", total: 62318 }),
    line({ event: "progress", done: 31159, total: 62318, kind: "PNG", file: 'assets/a "b".png' }),
    line({ event: "note", file: "x.json", note: "comments stripped" }),
    line({
      event: "done", files: 62318, optimized: 10322, notes: 1, input_bytes: 99627662, output_bytes: 68838138,
      dedup_bytes: 7048101, ms: 13100, out: "/packs/My Pack-compack.zip", sha1: "5b8f0d841b0116b76b944d5ba84c56ad9b8f16b3",
    }),
    { kind: "pack_exit", code: 0 },
  ]);

  const argv = packArgv(cmds);
  assert.equal(argv[0], "/opt/compack/assets/compack");
  for (const flag of ["-events", "-png-lossy=true", "-png-protect-alpha=true", "-ogg-lossy=true", "-json-minify=true"]) {
    assert.ok(argv.includes(flag), `${flag} in ${argv}`);
  }
  assert.deepEqual(argv.slice(-3), ["-out", "/packs/My Pack-compack.zip", "/packs/My Pack.zip"]);
  assert.ok(argv.length <= 16, "the SDK spawns at most 16 arguments");

  const progress = models[models.length - 4];
  assert.equal(progress.done, 31159);
  assert.equal(progress.total, 62318);
  assert.equal(text(progress.currentKind), "PNG");
  assert.equal(text(progress.currentFile), 'assets/a "b".png');

  assert.equal(last.run, "done");
  assert.equal(last.noteCount, 1);
  assert.equal(text(last.notes[0].text), "x.json: comments stripped");
  assert.equal(last.files, 62318);
  assert.equal(last.inputBytes, 99627662);
  assert.equal(last.outputBytes, 68838138);
  assert.equal(last.millis, 13100);
  assert.equal(text(last.outPath), "/packs/My Pack-compack.zip");
  assert.equal(text(last.sha1), "5b8f0d841b0116b76b944d5ba84c56ad9b8f16b3");
});

test("an output folder replaces the pack's own", () => {
  const { cmds } = run([
    linux,
    typeInput("/packs/pack"),
    { kind: "edit_output", edit: { kind: "insert_text", text: b("/srv/www/") } },
    { kind: "pack" },
  ]);
  assert.deepEqual(packArgv(cmds).slice(-3), ["-out", "/srv/www/pack-compack.zip", "/packs/pack"]);
});

test("failures, stops and a missing CLI end the run", () => {
  const failed = run([linux, typeInput("/p.zip"), { kind: "pack" }, line({ event: "error", message: "stat input: no such file" }), { kind: "pack_exit", code: 1 }]).last;
  assert.equal(failed.run, "failed");
  assert.equal(text(failed.message), "stat input: no such file");

  const crashed = run([linux, typeInput("/p.zip"), { kind: "pack" }, { kind: "pack_exit", code: 2 }]).last;
  assert.equal(text(crashed.message), "compack exited with code 2");

  const stopped = run([linux, typeInput("/p.zip"), { kind: "pack" }, { kind: "cancel_pack" }, { kind: "pack_err", reason: b("cancelled") }]);
  assert.ok(stopped.cmds.some((c) => c.startsWith("cancel pack")), stopped.cmds.join("\n"));
  assert.equal(stopped.last.run, "stopped");

  const missing = run([linux, typeInput("/p.zip"), { kind: "pack" }, { kind: "pack_err", reason: b("spawn_failed") }]).last;
  assert.match(text(missing.message), /\/opt\/compack\/assets\/compack/);
});

test("nothing packs before the platform is known or without a pack", () => {
  assert.ok(!packArgvOrNull(run([typeInput("/p.zip"), { kind: "pack" }]).cmds));
  assert.ok(!packArgvOrNull(run([linux, { kind: "pack" }]).cmds));
});

function packArgvOrNull(cmds) {
  return cmds.some((c) => c.startsWith('spawn key="pack"'));
}

test("presets set the lossy steps and turn every lossless pass on", () => {
  const { models } = run([
    linux,
    { kind: "toggle_json" },
    { kind: "preset_lossless" },
    { kind: "preset_smallest" },
    { kind: "preset_balanced" },
  ]);
  const [, custom, lossless, smallest, balanced] = models.slice(-5);
  assert.equal(custom.jsonMinify, false);
  assert.deepEqual([lossless.pngLossy, lossless.oggLossy, lossless.jsonMinify], [false, false, true]);
  assert.deepEqual([smallest.pngLossy, smallest.oggLossy], [true, true]);
  assert.deepEqual([balanced.pngLossy, balanced.oggLossy], [true, false]);
  // Each preset remounts the option switches (they keep their own state).
  assert.equal(balanced.optionsRev, lossless.optionsRev + 2);
});

test("settings are found through the environment, loaded, then saved", () => {
  const { cmds, last } = run([
    { kind: "probe_err", reason: b("spawn_failed") }, // no sh: Windows
    { kind: "env_home", value: b("C:\\Users\\me") },
    { kind: "env_appdata", value: b("C:\\Users\\me\\AppData\\Roaming\\") },
    { kind: "toggle_text" },
    { advance: 1000 }, // a save before the settings were read must not happen
    { kind: "settings_loaded", body: b("input=C:\\packs\\a.zip\r\nogg_lossy=1\njson_minify=0\n") },
    { kind: "toggle_png_strip" },
    { advance: 1000 },
    { kind: "pack" },
  ]);

  const reads = cmds.filter((c) => c.startsWith("read_file"));
  assert.match(reads[reads.length - 1], /AppData\\\\Roaming\\\\compack\\\\desktop\.conf/);
  const writes = cmds.filter((c) => c.startsWith("write_file"));
  assert.equal(writes.length, 1, writes.join("\n"));
  const saved = text(field(writes[0], "bytes"));
  for (const kv of ["input=C:\\packs\\a.zip", "ogg_lossy=1", "json_minify=0", "png_strip_meta=0"]) {
    assert.ok(saved.split("\n").includes(kv), `${kv} in\n${saved}`);
  }

  assert.equal(last.platform, "windows");
  assert.equal(last.optionsRev, 1);
  assert.deepEqual(packArgv(cmds).slice(0, 1).concat(packArgv(cmds).slice(-3)), [
    "assets\\compack.exe", "-out", "C:\\packs\\a-compack.zip", "C:\\packs\\a.zip",
  ]);
});

test("the update check compares versions", () => {
  const newer = run([{ kind: "update_fetched", status: 200, body: b('{"tag_name":"v1.10.0","name":"x"}') }]).last;
  assert.equal(newer.updateAvailable, true);
  const same = run([{ kind: "update_fetched", status: 200, body: b('{"tag_name":"v1.1.0"}') }]).last;
  assert.equal(same.updateAvailable, false);
  const junk = run([{ kind: "update_fetched", status: 404, body: b('{"message":"Not Found"}') }]).last;
  assert.equal(junk.updateAvailable, false);
});
