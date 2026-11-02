// compack desktop app — Model, Msg, update.
//
// A tiny front-end for the `compack` Minecraft resource/data pack optimizer:
// pick an input pack (a folder or a ready .zip) + an output folder, toggle
// the optimization passes, and spawn compack (which writes a `<input name>
// -compack.zip` into the chosen folder). compack runs with -events and reports
// progress and its summary as JSON lines on stdout. The paths and toggles are
// saved between launches; the GitHub release feed is checked for a newer
// release.

import { Cmd, asciiBytes, type EnvMsg } from "@native-sdk/core";
import { applyTextInputEvent, type TextEditState, type TextInputEvent } from "@native-sdk/core/text";

export type Bytes = Uint8Array;

export type Platform = "unknown" | "linux" | "mac" | "windows";

// The in-flight file picker target, and the Linux picker tried (zenity, then
// kdialog when zenity is not installed).
export type PickerTarget = "none" | "input_folder" | "input_file" | "output";
export type PickerTool = "zenity" | "kdialog";

export type RunState = "idle" | "running" | "done" | "failed" | "stopped";

// The option switches, keyed by optionsRev (see optionViews).
export interface OptionView {
  readonly rev: number;
}

// A compack note (a file it could not optimize), shown under the result.
export interface Note {
  readonly text: Bytes;
}

const RELEASE_PAGE = asciiBytes("https://github.com/qore-games/compack/releases/latest");
const REPO_PAGE = asciiBytes("https://github.com/qore-games/compack");

const TEXT_CAP = 4096;
const MAX_NOTES = 5;

// The app's own version — kept in sync with app.zon `.version` and with the
// GitHub release tags. The update banner compares this against the latest
// release `tag_name`.
const CURRENT_VERSION = asciiBytes("1.1.0");

// Boot probe (macOS / Linux): the OS on the first line, then the bundled
// compack CLI next to the app executable (the spawning app is the shell's
// parent): a packaged .app keeps it in Contents/Resources/assets, the Linux
// archive in resources/, the AppImage in AppDir/assets, a dev run in ./assets.
const PROBE = asciiBytes('uname -s; exe=$(readlink -f "/proc/$PPID/exe" 2>/dev/null || ps -o comm= -p "$PPID"); d=$(dirname "$exe"); for c in "$d/assets/compack" "$d/../Resources/assets/compack" "$d/../resources/compack" "$d/../../assets/compack" "$PWD/assets/compack"; do if [ -x "$c" ]; then echo "$c"; break; fi; done');

// ---------------------------------------------------------------------------
// Model

export interface Model {
  // host: detected by the boot probe
  readonly platform: Platform;
  readonly binPath: Bytes;

  // settings file (paths + toggles), found through the environment
  readonly settingsPath: Bytes;
  readonly settingsRank: number;
  readonly settingsLoaded: boolean;

  // editable path fields
  readonly inputEditor: TextEditState;
  readonly outputEditor: TextEditState;

  // optimization toggles (match compack's defaults)
  readonly pngRecompress: boolean;
  readonly pngStripMeta: boolean;
  readonly pngLossy: boolean;
  readonly pngProtectAlpha: boolean;
  readonly ogg: boolean;
  readonly oggStripComments: boolean;
  readonly oggLossy: boolean;
  readonly jsonMinify: boolean;
  readonly textMinify: boolean;
  // Bumped when the options change other than by their own switch.
  readonly optionsRev: number;

  readonly pickerTarget: PickerTarget;
  readonly pickerTool: PickerTool;

  // run state, fed by compack's -events lines
  readonly run: RunState;
  readonly done: number;
  readonly total: number;
  readonly currentKind: Bytes;
  readonly currentFile: Bytes;
  readonly message: Bytes;
  readonly notes: readonly Note[];
  readonly noteCount: number;
  readonly files: number;
  readonly optimized: number;
  readonly inputBytes: number;
  readonly outputBytes: number;
  readonly dedupBytes: number;
  readonly millis: number;
  readonly outPath: Bytes;
  readonly sha1: Bytes;

  // update banner
  readonly updateAvailable: boolean;
  readonly updateDismissed: boolean;
  readonly latestTag: Bytes;
}

export type Msg =
  | { readonly kind: "edit_input"; readonly edit: TextInputEvent }
  | { readonly kind: "edit_output"; readonly edit: TextInputEvent }
  | { readonly kind: "browse_input_folder" }
  | { readonly kind: "browse_input_file" }
  | { readonly kind: "browse_output" }
  | { readonly kind: "picker_exit"; readonly code: number; readonly output: Bytes }
  | { readonly kind: "picker_err"; readonly reason: Bytes }
  | { readonly kind: "toggle_png_recompress" }
  | { readonly kind: "toggle_png_strip" }
  | { readonly kind: "toggle_png_lossy" }
  | { readonly kind: "toggle_png_protect_alpha" }
  | { readonly kind: "toggle_ogg" }
  | { readonly kind: "toggle_ogg_strip" }
  | { readonly kind: "toggle_ogg_lossy" }
  | { readonly kind: "toggle_json" }
  | { readonly kind: "toggle_text" }
  | { readonly kind: "preset_lossless" }
  | { readonly kind: "preset_balanced" }
  | { readonly kind: "preset_smallest" }
  | { readonly kind: "pack" }
  | { readonly kind: "cancel_pack" }
  | { readonly kind: "pack_line"; readonly line: Bytes }
  | { readonly kind: "pack_exit"; readonly code: number }
  | { readonly kind: "pack_err"; readonly reason: Bytes }
  | { readonly kind: "show_in_folder" }
  | { readonly kind: "copy_out_path" }
  | { readonly kind: "copy_sha1" }
  | { readonly kind: "probe_exit"; readonly code: number; readonly output: Bytes }
  | { readonly kind: "probe_err"; readonly reason: Bytes }
  | { readonly kind: "env_home"; readonly value: Bytes }
  | { readonly kind: "env_xdg_config"; readonly value: Bytes }
  | { readonly kind: "env_appdata"; readonly value: Bytes }
  | { readonly kind: "settings_loaded"; readonly body: Bytes }
  | { readonly kind: "settings_load_failed"; readonly reason: Bytes }
  | { readonly kind: "save_now"; readonly at: number }
  | { readonly kind: "settings_saved" }
  | { readonly kind: "settings_save_failed"; readonly reason: Bytes }
  | { readonly kind: "update_fetched"; readonly status: number; readonly body: Bytes }
  | { readonly kind: "update_fetch_err"; readonly reason: Bytes }
  | { readonly kind: "dismiss_update" }
  | { readonly kind: "open_release" }
  | { readonly kind: "open_github" }
  | { readonly kind: "open_exit"; readonly code: number }
  | { readonly kind: "open_err"; readonly reason: Bytes };

// The settings file lives in the user's config folder: APPDATA (Windows)
// wins over XDG_CONFIG_HOME, which wins over HOME/.config.
export const envMsgs: readonly EnvMsg<Msg>[] = [
  { env: "HOME", msg: "env_home" },
  { env: "XDG_CONFIG_HOME", msg: "env_xdg_config" },
  { env: "APPDATA", msg: "env_appdata" },
] as const;

// Update-only / effect-routed state and messages (consumed from update or
// via the bound helpers below, never directly bound in markup).
export const viewUnbound = [
  "platform", "binPath", "settingsPath", "settingsRank", "settingsLoaded",
  "pickerTarget", "pickerTool",
  "run", "done", "total", "currentKind", "currentFile", "message",
  "optionsRev", "noteCount", "files", "optimized", "inputBytes", "outputBytes", "dedupBytes", "millis", "outPath",
  "updateAvailable", "updateDismissed", "latestTag",
  "picker_exit", "picker_err",
  "pack_line", "pack_exit", "pack_err",
  "probe_exit", "probe_err",
  "env_home", "env_xdg_config", "env_appdata",
  "settings_loaded", "settings_load_failed", "save_now", "settings_saved", "settings_save_failed",
  "update_fetched", "update_fetch_err",
  "open_exit", "open_err",
] as const;

function freshEditor(): TextEditState {
  return { text: new Uint8Array(0), selection: { anchor: 0, focus: 0 }, composition: null };
}

function editorForText(text: Bytes): TextEditState {
  return { text: text, selection: { anchor: text.length, focus: text.length }, composition: null };
}

export function initialModel(): [Model, Cmd<Msg>] {
  const model: Model = {
    platform: "unknown",
    binPath: new Uint8Array(0),
    settingsPath: new Uint8Array(0),
    settingsRank: 0,
    settingsLoaded: false,

    inputEditor: freshEditor(),
    outputEditor: freshEditor(),

    pngRecompress: true,
    pngStripMeta: true,
    pngLossy: true,
    pngProtectAlpha: true,
    ogg: true,
    oggStripComments: true,
    oggLossy: false,
    jsonMinify: true,
    textMinify: true,
    optionsRev: 0,

    pickerTarget: "none",
    pickerTool: "zenity",

    run: "idle",
    done: 0,
    total: 0,
    currentKind: new Uint8Array(0),
    currentFile: new Uint8Array(0),
    message: new Uint8Array(0),
    notes: [],
    noteCount: 0,
    files: 0,
    optimized: 0,
    inputBytes: 0,
    outputBytes: 0,
    dedupBytes: 0,
    millis: 0,
    outPath: new Uint8Array(0),
    sha1: new Uint8Array(0),

    updateAvailable: false,
    updateDismissed: false,
    latestTag: new Uint8Array(0),
  };
  // Boot: probe the host and fetch the release feed in parallel.
  return [
    model,
    Cmd.batch<Msg>([
      Cmd.spawn([asciiBytes("sh"), asciiBytes("-c"), PROBE], {
        key: "probe",
        collect: true,
        exit: "probe_exit",
        err: "probe_err",
      }),
      Cmd.fetch(
        { url: asciiBytes("https://api.github.com/repos/qore-games/compack/releases/latest"), method: "GET", headers: { "Accept": asciiBytes("application/vnd.github+json"), "User-Agent": asciiBytes("compack-app") } },
        { key: "release", ok: "update_fetched", err: "update_fetch_err" },
      ),
    ]),
  ];
}

// ---------------------------------------------------------------------------
// Helpers (pure)

function concatBytes(parts: readonly Bytes[]): Bytes {
  let total = 0;
  for (let i = 0; i < parts.length; i++) total += parts[i].length;
  const out = new Uint8Array(total);
  let off = 0;
  for (let i = 0; i < parts.length; i++) {
    out.set(parts[i], off);
    off += parts[i].length;
  }
  return out;
}

function bytesEqual(a: Bytes, b: Bytes): boolean {
  return a.length === b.length && a.startsWith(b);
}

function inputPath(model: Model): Bytes {
  return model.inputEditor.text.trim();
}

function isSep(b: number): boolean {
  return b === 0x2f || b === 0x5c;
}

// Strip trailing path separators ('/' or '\').
function trimTrailingSep(text: Bytes): Bytes {
  let end = text.length;
  while (end > 1 && isSep(text[end - 1])) end -= 1;
  return text.subarray(0, end);
}

// The last path segment of `path` (the basename); the extension is kept.
function basename(path: Bytes): Bytes {
  const t = trimTrailingSep(path);
  let start = t.length;
  while (start > 0 && !isSep(t[start - 1])) start -= 1;
  return t.subarray(start);
}

// Everything before the last path segment, without the separator ("" when
// the path has no folder part).
function dirname(path: Bytes): Bytes {
  const t = trimTrailingSep(path);
  let end = t.length;
  while (end > 0 && !isSep(t[end - 1])) end -= 1;
  if (end === 0) return new Uint8Array(0);
  return end === 1 ? t.subarray(0, 1) : t.subarray(0, end - 1);
}

function sepFor(model: Model): Bytes {
  return model.platform === "windows" ? asciiBytes("\\") : asciiBytes("/");
}

// The folder compack writes to: the chosen one, else the input's folder.
function outputDir(model: Model): Bytes {
  const dir = model.outputEditor.text.trim();
  return dir.length > 0 ? trimTrailingSep(dir) : dirname(inputPath(model));
}

// computeOutPath builds the `-out` value passed to compack: the output folder
// joined with the input pack's basename plus the `-compack.zip` suffix.
function computeOutPath(model: Model): Bytes {
  let name = basename(inputPath(model));
  if (name.toLowerCase().endsWith(asciiBytes(".zip"))) name = name.subarray(0, name.length - 4);
  if (name.length === 0) name = asciiBytes("pack");
  const dir = outputDir(model);
  if (dir.length === 0) return concatBytes([name, asciiBytes("-compack.zip")]);
  return concatBytes([dir, sepFor(model), name, asciiBytes("-compack.zip")]);
}

function compackBin(model: Model): Bytes {
  if (model.binPath.length > 0) return model.binPath;
  return model.platform === "windows" ? asciiBytes("assets\\compack.exe") : asciiBytes("assets/compack");
}

function flagBool(name: Bytes, value: boolean): Bytes {
  return concatBytes([asciiBytes("-"), name, value ? asciiBytes("=true") : asciiBytes("=false")]);
}

function canPack(model: Model): boolean {
  return model.run !== "running" && model.platform !== "unknown" && inputPath(model).length > 0;
}

// Integer division without floats (the subset keeps the two apart).
function intDiv(n: number, d: number): number {
  let q = 0;
  let r = n;
  while (r >= d) {
    let step = d;
    let count = 1;
    while (step + step <= r) {
      step += step;
      count += count;
    }
    r -= step;
    q += count;
  }
  return q;
}

// "<whole>.<tenth>" of n / unit, rounded.
function tenths(n: number, unit: number): Bytes {
  const t = intDiv(n * 10 + intDiv(unit, 2), unit);
  return asciiBytes(`${intDiv(t, 10)}.${t % 10}`);
}

function formatBytes(n: number): Bytes {
  if (n < 1024) return asciiBytes(`${n} B`);
  if (n < 1048576) return concatBytes([tenths(n, 1024), asciiBytes(" KB")]);
  if (n < 1073741824) return concatBytes([tenths(n, 1048576), asciiBytes(" MB")]);
  return concatBytes([tenths(n, 1073741824), asciiBytes(" GB")]);
}

// 62318 -> "62,318"
function formatCount(n: number): Bytes {
  if (n < 1000) return asciiBytes(`${n}`);
  const head = intDiv(n, 1000);
  return concatBytes([formatCount(head), asciiBytes(","), asciiBytes(`${n - head * 1000}`).padStart(3, asciiBytes("0"))]);
}

function parseDigits(text: Bytes): number {
  let v = 0;
  for (let i = 0; i < text.length; i++) {
    const b = text[i];
    if (b < 0x30 || b > 0x39) break;
    v = v * 10 + (b - 0x30);
  }
  return v;
}

// --- compack -events lines: flat JSON objects, one per line ---------------

// The integer value of `key` (a `"name":` literal), or 0 when absent.
function jsonInt(line: Bytes, key: Bytes): number {
  const at = line.indexOf(key);
  if (at < 0) return 0;
  return parseDigits(line.subarray(at + key.length));
}

// The string value of `key` (a `"name":` literal), unescaped, or empty.
// \uXXXX escapes (control characters) read as '?'.
function jsonString(line: Bytes, key: Bytes): Bytes {
  const at = line.indexOf(key);
  const start = at + key.length + 1;
  if (at < 0 || start > line.length || line[start - 1] !== 0x22) return new Uint8Array(0);
  // Measure, then fill.
  let n = 0;
  let i = start;
  while (i < line.length && line[i] !== 0x22) {
    i += line[i] !== 0x5c ? 1 : i + 1 < line.length && line[i + 1] === 0x75 ? 6 : 2;
    n += 1;
  }
  const out = new Uint8Array(n);
  let o = 0;
  i = start;
  while (o < n) {
    const b = line[i];
    if (b !== 0x5c) {
      out[o] = b;
      i += 1;
    } else {
      const e = i + 1 < line.length ? line[i + 1] : 0x3f;
      if (e === 0x75) {
        out[o] = 0x3f;
        i += 6;
      } else {
        out[o] = e === 0x6e || e === 0x74 || e === 0x72 ? 0x20 : e;
        i += 2;
      }
    }
    o += 1;
  }
  return out;
}

// --- settings file: `key=value` lines ---------------------------------------

function boolText(v: boolean): Bytes {
  return v ? asciiBytes("1") : asciiBytes("0");
}

function settingsBytes(model: Model): Bytes {
  const nl = asciiBytes("\n");
  return concatBytes([
    asciiBytes("input="), inputPath(model), nl,
    asciiBytes("output="), model.outputEditor.text.trim(), nl,
    asciiBytes("png_recompress="), boolText(model.pngRecompress), nl,
    asciiBytes("png_strip_meta="), boolText(model.pngStripMeta), nl,
    asciiBytes("png_lossy="), boolText(model.pngLossy), nl,
    asciiBytes("png_protect_alpha="), boolText(model.pngProtectAlpha), nl,
    asciiBytes("ogg="), boolText(model.ogg), nl,
    asciiBytes("ogg_strip_comments="), boolText(model.oggStripComments), nl,
    asciiBytes("ogg_lossy="), boolText(model.oggLossy), nl,
    asciiBytes("json_minify="), boolText(model.jsonMinify), nl,
    asciiBytes("text_minify="), boolText(model.textMinify), nl,
  ]);
}

function applySettings(model: Model, body: Bytes): Model {
  let next = model;
  const lines = body.split(asciiBytes("\n"));
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trimEnd();
    const eq = line.indexOf(asciiBytes("="));
    if (eq < 0) continue;
    const key = line.subarray(0, eq);
    const value = line.subarray(eq + 1);
    const on = bytesEqual(value, asciiBytes("1"));
    if (bytesEqual(key, asciiBytes("input"))) next = { ...next, inputEditor: editorForText(value) };
    else if (bytesEqual(key, asciiBytes("output"))) next = { ...next, outputEditor: editorForText(value) };
    else if (bytesEqual(key, asciiBytes("png_recompress"))) next = { ...next, pngRecompress: on };
    else if (bytesEqual(key, asciiBytes("png_strip_meta"))) next = { ...next, pngStripMeta: on };
    else if (bytesEqual(key, asciiBytes("png_lossy"))) next = { ...next, pngLossy: on };
    else if (bytesEqual(key, asciiBytes("png_protect_alpha"))) next = { ...next, pngProtectAlpha: on };
    else if (bytesEqual(key, asciiBytes("ogg"))) next = { ...next, ogg: on };
    else if (bytesEqual(key, asciiBytes("ogg_strip_comments"))) next = { ...next, oggStripComments: on };
    else if (bytesEqual(key, asciiBytes("ogg_lossy"))) next = { ...next, oggLossy: on };
    else if (bytesEqual(key, asciiBytes("json_minify"))) next = { ...next, jsonMinify: on };
    else if (bytesEqual(key, asciiBytes("text_minify"))) next = { ...next, textMinify: on };
  }
  return next;
}

// --- compression presets ----------------------------------------------------

// A preset sets the two lossy steps and turns every lossless pass on.
function withPreset(model: Model, pngLossy: boolean, oggLossy: boolean): Model {
  return {
    ...model,
    pngLossy: pngLossy,
    pngProtectAlpha: true,
    oggLossy: oggLossy,
    pngRecompress: true,
    pngStripMeta: true,
    ogg: true,
    oggStripComments: true,
    jsonMinify: true,
    textMinify: true,
  };
}

function isPreset(model: Model, pngLossy: boolean, oggLossy: boolean): boolean {
  return model.pngLossy === pngLossy && model.oggLossy === oggLossy && (!pngLossy || model.pngProtectAlpha) &&
    model.pngRecompress && model.pngStripMeta && model.ogg && model.oggStripComments && model.jsonMinify && model.textMinify;
}

// --- update check -------------------------------------------------------------

// Parse a "major.minor.patch" bytes value into three integers. Missing parts
// count as zero; anything but digits and dots is not a version.
interface Semver { readonly ok: boolean; readonly major: number; readonly minor: number; readonly patch: number; }

function parseSemver(text: Bytes): Semver {
  for (let i = 0; i < text.length; i++) {
    const b = text[i];
    if (b !== 0x2e && (b < 0x30 || b > 0x39)) return { ok: false, major: 0, minor: 0, patch: 0 };
  }
  const f = text.split(asciiBytes("."));
  return {
    ok: text.length > 0,
    major: parseDigits(f[0]),
    minor: f.length > 1 ? parseDigits(f[1]) : 0,
    patch: f.length > 2 ? parseDigits(f[2]) : 0,
  };
}

function isNewer(remote: Semver, current: Semver): boolean {
  if (remote.major !== current.major) return remote.major > current.major;
  if (remote.minor !== current.minor) return remote.minor > current.minor;
  return remote.patch > current.patch;
}

function stripLeadingV(text: Bytes): Bytes {
  return text.length > 0 && text[0] === 0x76 ? text.subarray(1) : text;
}

// ---------------------------------------------------------------------------
// Update

export function update(model: Model, msg: Msg): Model | [Model, Cmd<Msg>] {
  switch (msg.kind) {
    case "edit_input": {
      const next = applyTextInputEvent(model.inputEditor, msg.edit, TEXT_CAP);
      if (next === null) return model;
      return [{ ...model, inputEditor: next }, Cmd.delay("save", 600, "save_now")];
    }
    case "edit_output": {
      const next = applyTextInputEvent(model.outputEditor, msg.edit, TEXT_CAP);
      if (next === null) return model;
      return [{ ...model, outputEditor: next }, Cmd.delay("save", 600, "save_now")];
    }

    case "browse_input_folder": {
      if (model.run === "running") return model;
      const start: Model = { ...model, pickerTarget: "input_folder" };
      if (model.platform === "linux" && model.pickerTool === "kdialog") {
        return [start, Cmd.spawn([asciiBytes("kdialog"), asciiBytes("--title"), asciiBytes("Select resource pack folder"), asciiBytes("--getexistingdirectory"), dirname(inputPath(model))], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "linux") {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--directory"), asciiBytes("--title=Select resource pack folder")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "mac") {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose folder with prompt "Select resource pack folder")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "windows") {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description='Select resource pack folder'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.SelectedPath)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "browse_input_file": {
      if (model.run === "running") return model;
      const start: Model = { ...model, pickerTarget: "input_file" };
      if (model.platform === "linux" && model.pickerTool === "kdialog") {
        return [start, Cmd.spawn([asciiBytes("kdialog"), asciiBytes("--title"), asciiBytes("Select resource pack .zip"), asciiBytes("--getopenfilename"), dirname(inputPath(model)), asciiBytes("*.zip")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "linux") {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--file-filter=Zip files | *.zip"), asciiBytes("--file-filter=All files | *"), asciiBytes("--title=Select resource pack .zip")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "mac") {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose file with prompt "Select resource pack .zip")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "windows") {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.OpenFileDialog; $f.Filter='Zip files (*.zip)|*.zip|All files (*.*)|*.*'; $f.Title='Select resource pack .zip'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.FileName)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "browse_output": {
      if (model.run === "running") return model;
      const start: Model = { ...model, pickerTarget: "output" };
      if (model.platform === "linux" && model.pickerTool === "kdialog") {
        return [start, Cmd.spawn([asciiBytes("kdialog"), asciiBytes("--title"), asciiBytes("Save pack to folder"), asciiBytes("--getexistingdirectory"), outputDir(model)], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "linux") {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--directory"), asciiBytes("--title=Save pack to folder")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "mac") {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose folder with prompt "Save pack to folder")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platform === "windows") {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description='Save pack to folder'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.SelectedPath)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "picker_exit": {
      const picked = msg.code !== 0 ? new Uint8Array(0) : msg.output.trim();
      const target = model.pickerTarget;
      const next: Model = { ...model, pickerTarget: "none" };
      if (picked.length === 0) return next;
      if (target === "output") {
        return [{ ...next, outputEditor: editorForText(picked) }, Cmd.delay("save", 600, "save_now")];
      }
      return [{ ...next, inputEditor: editorForText(picked) }, Cmd.delay("save", 600, "save_now")];
    }
    case "picker_err": {
      // zenity missing: try kdialog next time (and tell the user now).
      if (model.platform === "linux" && model.pickerTool === "zenity" && bytesEqual(msg.reason, asciiBytes("spawn_failed"))) {
        return { ...model, pickerTarget: "none", pickerTool: "kdialog", run: "failed", message: asciiBytes("zenity is not installed: press Browse again to use kdialog, or paste the path.") };
      }
      return { ...model, pickerTarget: "none", run: "failed", message: concatBytes([asciiBytes("Could not open a file dialog ("), msg.reason, asciiBytes("): paste the path instead.")]) };
    }

    case "toggle_png_recompress": return [{ ...model, pngRecompress: !model.pngRecompress }, Cmd.delay("save", 600, "save_now")];
    case "toggle_png_strip": return [{ ...model, pngStripMeta: !model.pngStripMeta }, Cmd.delay("save", 600, "save_now")];
    case "toggle_png_lossy": return [{ ...model, pngLossy: !model.pngLossy }, Cmd.delay("save", 600, "save_now")];
    case "toggle_png_protect_alpha": return [{ ...model, pngProtectAlpha: !model.pngProtectAlpha }, Cmd.delay("save", 600, "save_now")];
    case "toggle_ogg": return [{ ...model, ogg: !model.ogg }, Cmd.delay("save", 600, "save_now")];
    case "toggle_ogg_strip": return [{ ...model, oggStripComments: !model.oggStripComments }, Cmd.delay("save", 600, "save_now")];
    case "toggle_ogg_lossy": return [{ ...model, oggLossy: !model.oggLossy }, Cmd.delay("save", 600, "save_now")];
    case "toggle_json": return [{ ...model, jsonMinify: !model.jsonMinify }, Cmd.delay("save", 600, "save_now")];
    case "toggle_text": return [{ ...model, textMinify: !model.textMinify }, Cmd.delay("save", 600, "save_now")];
    case "preset_lossless": return [{ ...withPreset(model, false, false), optionsRev: model.optionsRev + 1 }, Cmd.delay("save", 600, "save_now")];
    case "preset_balanced": return [{ ...withPreset(model, true, false), optionsRev: model.optionsRev + 1 }, Cmd.delay("save", 600, "save_now")];
    case "preset_smallest": return [{ ...withPreset(model, true, true), optionsRev: model.optionsRev + 1 }, Cmd.delay("save", 600, "save_now")];

    case "pack": {
      if (!canPack(model)) return model;
      const start: Model = {
        ...model,
        run: "running",
        done: 0,
        total: 0,
        currentKind: new Uint8Array(0),
        currentFile: new Uint8Array(0),
        message: new Uint8Array(0),
        notes: [],
        noteCount: 0,
        outPath: new Uint8Array(0),
        sha1: new Uint8Array(0),
      };
      // Paths are separate argv elements, so spaces and unicode never need
      // quoting. Progress and the summary come back as JSON lines (-events).
      return [
        start,
        Cmd.spawn(
          [
            compackBin(model),
            asciiBytes("-events"),
            flagBool(asciiBytes("png-recompress"), model.pngRecompress),
            flagBool(asciiBytes("png-strip-meta"), model.pngStripMeta),
            flagBool(asciiBytes("png-lossy"), model.pngLossy),
            flagBool(asciiBytes("png-protect-alpha"), model.pngProtectAlpha),
            flagBool(asciiBytes("ogg"), model.ogg),
            flagBool(asciiBytes("ogg-strip-comments"), model.oggStripComments),
            flagBool(asciiBytes("ogg-lossy"), model.oggLossy),
            flagBool(asciiBytes("json-minify"), model.jsonMinify),
            flagBool(asciiBytes("text-minify"), model.textMinify),
            asciiBytes("-out"), computeOutPath(model), inputPath(model),
          ],
          { key: "pack", line: "pack_line", exit: "pack_exit", err: "pack_err" },
        ),
      ];
    }
    case "cancel_pack": {
      if (model.run !== "running") return model;
      return [model, Cmd.cancel("pack")];
    }
    case "pack_line": {
      const line = msg.line;
      if (line.startsWith(asciiBytes('{"event":"progress"'))) {
        return {
          ...model,
          done: jsonInt(line, asciiBytes('"done":')),
          total: jsonInt(line, asciiBytes('"total":')),
          currentKind: jsonString(line, asciiBytes('"kind":')),
          currentFile: jsonString(line, asciiBytes('"file":')),
        };
      }
      if (line.startsWith(asciiBytes('{"event":"start"'))) {
        return { ...model, total: jsonInt(line, asciiBytes('"total":')) };
      }
      if (line.startsWith(asciiBytes('{"event":"note"'))) {
        const note: Note = { text: concatBytes([jsonString(line, asciiBytes('"file":')), asciiBytes(": "), jsonString(line, asciiBytes('"note":'))]) };
        const notes: readonly Note[] = model.notes.length < MAX_NOTES ? [...model.notes, note] : model.notes;
        return { ...model, notes: notes, noteCount: model.noteCount + 1 };
      }
      if (line.startsWith(asciiBytes('{"event":"done"'))) {
        return {
          ...model,
          files: jsonInt(line, asciiBytes('"files":')),
          optimized: jsonInt(line, asciiBytes('"optimized":')),
          inputBytes: jsonInt(line, asciiBytes('"input_bytes":')),
          outputBytes: jsonInt(line, asciiBytes('"output_bytes":')),
          dedupBytes: jsonInt(line, asciiBytes('"dedup_bytes":')),
          millis: jsonInt(line, asciiBytes('"ms":')),
          outPath: jsonString(line, asciiBytes('"out":')),
          sha1: jsonString(line, asciiBytes('"sha1":')),
        };
      }
      if (line.startsWith(asciiBytes('{"event":"error"'))) {
        return { ...model, message: jsonString(line, asciiBytes('"message":')) };
      }
      return model;
    }
    case "pack_exit": {
      if (msg.code === 0 && model.sha1.length > 0) return { ...model, run: "done" };
      const message = model.message.length > 0 ? model.message : asciiBytes(`compack exited with code ${msg.code}`);
      return { ...model, run: "failed", message: message };
    }
    case "pack_err": {
      if (bytesEqual(msg.reason, asciiBytes("cancelled"))) return { ...model, run: "stopped" };
      if (bytesEqual(msg.reason, asciiBytes("spawn_failed"))) {
        return { ...model, run: "failed", message: concatBytes([asciiBytes("Could not start the compack CLI at "), compackBin(model)]) };
      }
      return { ...model, run: "failed", message: concatBytes([asciiBytes("compack stopped: "), msg.reason]) };
    }

    case "show_in_folder": {
      if (model.outPath.length === 0) return model;
      if (model.platform === "mac") {
        return [model, Cmd.spawn([asciiBytes("open"), asciiBytes("-R"), model.outPath], { key: "open", exit: "open_exit", err: "open_err" })];
      }
      if (model.platform === "windows") {
        return [model, Cmd.spawn([asciiBytes("explorer"), concatBytes([asciiBytes("/select,"), model.outPath])], { key: "open", exit: "open_exit", err: "open_err" })];
      }
      return [model, Cmd.spawn([asciiBytes("xdg-open"), dirname(model.outPath)], { key: "open", exit: "open_exit", err: "open_err" })];
    }
    case "copy_out_path": return [model, Cmd.clipboardWrite(model.outPath)];
    case "copy_sha1": return [model, Cmd.clipboardWrite(model.sha1)];

    case "probe_exit": {
      const lines = msg.output.split(asciiBytes("\n"));
      const os = lines[0].trim();
      let platform: Platform = "windows";
      if (os.startsWith(asciiBytes("Darwin"))) platform = "mac";
      else if (os.startsWith(asciiBytes("Linux"))) platform = "linux";
      // A located CLI is only usable on macOS/Linux (an MSYS shell on
      // Windows answers with paths CreateProcess cannot run).
      const bin = platform !== "windows" && lines.length > 1 ? lines[1].trim() : new Uint8Array(0);
      return { ...model, platform: platform, binPath: bin };
    }
    case "probe_err": {
      // No `sh`: Windows.
      return { ...model, platform: "windows" };
    }

    case "env_home": {
      if (model.settingsRank >= 1) return model;
      const path = concatBytes([trimTrailingSep(msg.value), asciiBytes("/.config/compack/desktop.conf")]);
      return [
        { ...model, settingsRank: 1, settingsPath: path },
        Cmd.readFile(path, { key: "settings_load", ok: "settings_loaded", err: "settings_load_failed" }),
      ];
    }
    case "env_xdg_config": {
      if (model.settingsRank >= 2) return model;
      const path = concatBytes([trimTrailingSep(msg.value), asciiBytes("/compack/desktop.conf")]);
      return [
        { ...model, settingsRank: 2, settingsPath: path },
        Cmd.readFile(path, { key: "settings_load", ok: "settings_loaded", err: "settings_load_failed" }),
      ];
    }
    case "env_appdata": {
      if (model.settingsRank >= 3) return model;
      const path = concatBytes([trimTrailingSep(msg.value), asciiBytes("\\compack\\desktop.conf")]);
      return [
        { ...model, settingsRank: 3, settingsPath: path },
        Cmd.readFile(path, { key: "settings_load", ok: "settings_loaded", err: "settings_load_failed" }),
      ];
    }
    case "settings_loaded": {
      return { ...applySettings(model, msg.body), settingsLoaded: true, optionsRev: model.optionsRev + 1 };
    }
    case "settings_load_failed": {
      // not_found on a first launch: keep the defaults, start saving.
      return { ...model, settingsLoaded: true };
    }
    case "save_now": {
      // Never save before the stored settings were read: that would
      // overwrite them with the defaults.
      if (!model.settingsLoaded || model.settingsPath.length === 0) return model;
      return [model, Cmd.writeFile(model.settingsPath, settingsBytes(model), { key: "settings_save", ok: "settings_saved", err: "settings_save_failed" })];
    }
    case "settings_saved": return model;
    case "settings_save_failed": return model;

    case "update_fetched": {
      const tag = jsonString(msg.body, asciiBytes('"tag_name":'));
      if (tag.length === 0) return model;
      const remote = parseSemver(stripLeadingV(tag));
      const current = parseSemver(CURRENT_VERSION);
      return { ...model, updateAvailable: remote.ok && current.ok && isNewer(remote, current), latestTag: tag };
    }
    case "update_fetch_err": return model;
    case "dismiss_update": return { ...model, updateDismissed: true };

    case "open_release":
    case "open_github": {
      const url = msg.kind === "open_release" ? RELEASE_PAGE : REPO_PAGE;
      if (model.platform === "mac") {
        return [model, Cmd.spawn([asciiBytes("open"), url], { key: "open", exit: "open_exit", err: "open_err" })];
      }
      if (model.platform === "windows") {
        return [model, Cmd.spawn([asciiBytes("cmd"), asciiBytes("/c"), asciiBytes("start"), asciiBytes(""), url], { key: "open", exit: "open_exit", err: "open_err" })];
      }
      return [model, Cmd.spawn([asciiBytes("xdg-open"), url], { key: "open", exit: "open_exit", err: "open_err" })];
    }
    case "open_exit": return model;
    case "open_err": return model;
  }
}

// ---------------------------------------------------------------------------
// Derived values the markup binds

export function canPackNow(model: Model): boolean {
  return canPack(model);
}

export function isPacking(model: Model): boolean {
  return model.run === "running";
}

export function isDone(model: Model): boolean {
  return model.run === "done";
}

export function isFailed(model: Model): boolean {
  return model.run === "failed";
}

export function isStopped(model: Model): boolean {
  return model.run === "stopped";
}

// The option switches keep their own state once mounted, so they sit in a
// one-item list keyed by optionsRev: a preset or the saved settings remount
// them with the model's values.
export function optionViews(model: Model): readonly OptionView[] {
  return [{ rev: model.optionsRev }];
}

export function hasInput(model: Model): boolean {
  return inputPath(model).length > 0;
}

export function updateBannerVisible(model: Model): boolean {
  return model.updateAvailable && !model.updateDismissed;
}

export function latestVersionText(model: Model): Bytes {
  return model.latestTag;
}

export function versionText(model: Model): Bytes {
  if (model.platform === "unknown") return concatBytes([asciiBytes("v"), CURRENT_VERSION]);
  const os = model.platform === "mac" ? asciiBytes("macOS") : model.platform === "windows" ? asciiBytes("Windows") : asciiBytes("Linux");
  return concatBytes([asciiBytes("v"), CURRENT_VERSION, asciiBytes(" · "), os]);
}

// Where the pack will be written, shown under the output field.
export function outputPreviewText(model: Model): Bytes {
  return computeOutPath(model);
}

export function isLossless(model: Model): boolean {
  return isPreset(model, false, false);
}

export function isBalanced(model: Model): boolean {
  return isPreset(model, true, false);
}

export function isSmallest(model: Model): boolean {
  return isPreset(model, true, true);
}

export function presetHintText(model: Model): Bytes {
  if (isLossless(model)) return asciiBytes("Every pixel and sound kept as is.");
  if (isBalanced(model)) return asciiBytes("Lossy PNG palettes, sounds kept as is.");
  if (isSmallest(model)) return asciiBytes("Lossy PNG palettes, heavy sounds re-encoded.");
  return asciiBytes("Custom options.");
}

// The 0..1 fraction the progress bar binds. The subset keeps integers and
// fractions apart, so the whole percentage is built up in 0.01 steps.
export function progressValue(model: Model): number {
  let fraction = 0;
  for (let i = 0; i < percentDone(model); i++) fraction += 0.01;
  return fraction;
}

function percentDone(model: Model): number {
  return model.total > 0 ? intDiv(model.done * 100, model.total) : 0;
}

export function progressPercentText(model: Model): Bytes {
  return asciiBytes(`${percentDone(model)}%`);
}

export function progressCountText(model: Model): Bytes {
  if (model.total === 0) return asciiBytes("Reading the pack...");
  return concatBytes([formatCount(model.done), asciiBytes(" of "), formatCount(model.total), asciiBytes(" files")]);
}

export function progressFileText(model: Model): Bytes {
  return model.currentFile;
}

// "30.9% smaller"
export function resultSavedText(model: Model): Bytes {
  if (model.inputBytes === 0) return formatBytes(model.outputBytes);
  if (model.outputBytes > model.inputBytes) {
    return concatBytes([tenths((model.outputBytes - model.inputBytes) * 100, model.inputBytes), asciiBytes("% larger")]);
  }
  return concatBytes([tenths((model.inputBytes - model.outputBytes) * 100, model.inputBytes), asciiBytes("% smaller")]);
}

// "95.0 MB → 65.6 MB · 62,318 files · 13.0 s"
export function resultSizesText(model: Model): Bytes {
  const t = intDiv(model.millis + 50, 100);
  return concatBytes([
    formatBytes(model.inputBytes), asciiBytes(" → "), formatBytes(model.outputBytes), asciiBytes(" · "),
    formatCount(model.files), asciiBytes(" files · "), asciiBytes(`${intDiv(t, 10)}.${t % 10} s`),
  ]);
}

export function outNameText(model: Model): Bytes {
  return basename(model.outPath);
}

export function hasNotes(model: Model): boolean {
  return model.noteCount > 0;
}

export function notesTitleText(model: Model): Bytes {
  if (model.noteCount === 1) return asciiBytes("1 file kept as is:");
  if (model.noteCount <= MAX_NOTES) return asciiBytes(`${model.noteCount} files kept as is:`);
  return asciiBytes(`${model.noteCount} files kept as is (first ${MAX_NOTES}):`);
}
