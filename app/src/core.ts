// compack desktop app — Model, Msg, update.
//
// A tiny front-end for the `compack` Minecraft resource/data pack optimizer:
// pick an input pack (a folder or a ready .zip) + an output folder, toggle
// the optimization passes, and spawn compack (which writes a `<input name>
// -compack.zip` into the chosen folder). Also checks the GitHub release
// feed for a newer release and shows an update banner.

import { Cmd, asciiBytes } from "@native-sdk/core";
import { applyTextInputEvent, type TextEditState, type TextInputEvent } from "@native-sdk/core/text";

export type Bytes = Uint8Array;

// The in-flight file picker target (none / input / output folder).
export type PickerTarget = "none" | "input_folder" | "input_file" | "output";

const RELEASE_REPO = "qore-games/compack";
const RELEASE_PAGE = asciiBytes("https://github.com/qore-games/compack/releases/latest");

const TEXT_CAP = 4096;

// The app's own version — kept in sync with app.zon `.version` and with the
// GitHub release tags. The update banner compares this against the latest
// release `tag_name`.
const CURRENT_VERSION = asciiBytes("1.0.1");

const SHELL_MERGE = asciiBytes('exec "$0" "$@" 2>&1');

// ---------------------------------------------------------------------------
// Model

export interface Model {
  // platform detection (boot spawn of `uname -s`)
  readonly platformKnown: boolean;
  readonly platformIsLinux: boolean;
  readonly platformIsMac: boolean;
  readonly platformIsWindows: boolean;

  // editable path / value fields
  readonly inputEditor: TextEditState;
  readonly outputEditor: TextEditState;

  // optimization toggles (match compack's defaults)
  readonly pngRecompress: boolean;
  readonly pngStripMeta: boolean;
  readonly pngLossy: boolean;
  readonly pngProtectAlpha: boolean;
  readonly ogg: boolean;
  readonly oggStripComments: boolean;
  readonly jsonMinify: boolean;
  readonly textMinify: boolean;

  // which picker is in flight (saved so the single picker exit handler knows
  // which editor's text to replace): "none" | "input" | "output"
  readonly pickerTarget: PickerTarget;

  // run state
  readonly running: boolean;
  readonly result: Bytes;
  readonly exitCode: number;
  readonly hadResult: boolean;
  readonly progressLine: Bytes;
  readonly filesProcessed: number;
  readonly outputSize: number;

  // update banner
  readonly updatePending: boolean;
  readonly updateDismissed: boolean;
  readonly updateChecked: boolean;
  readonly updateAvailable: boolean;
  readonly latestTag: Bytes;
  readonly updateCopied: boolean;

  // optimization disclosure
  readonly optimizationOpen: boolean;
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
  | { readonly kind: "toggle_json" }
  | { readonly kind: "toggle_text" }
  | { readonly kind: "toggle_optimization" }
  | { readonly kind: "pack" }
  | { readonly kind: "cancel_pack" }
  | { readonly kind: "pack_line"; readonly line: Bytes }
  | { readonly kind: "pack_exit"; readonly code: number }
  | { readonly kind: "pack_err"; readonly reason: Bytes }
  | { readonly kind: "stat_exit"; readonly code: number; readonly output: Bytes }
  | { readonly kind: "stat_err"; readonly reason: Bytes }
  | { readonly kind: "clear_result" }
  | { readonly kind: "uname_exit"; readonly code: number; readonly output: Bytes }
  | { readonly kind: "uname_err"; readonly reason: Bytes }
  | { readonly kind: "check_update" }
  | { readonly kind: "update_fetched"; readonly status: number; readonly body: Bytes }
  | { readonly kind: "update_fetch_err"; readonly reason: Bytes }
  | { readonly kind: "dismiss_update" }
  | { readonly kind: "copy_update_link" }
  | { readonly kind: "open_github" }
  | { readonly kind: "open_url_exit"; readonly code: number }
  | { readonly kind: "open_url_err"; readonly reason: Bytes }
  | { readonly kind: "quit" };

// Update-only / effect-routed state and messages (consumed from update or
// via the bound helpers below, never directly bound in markup).
export const viewUnbound = [
  "pickerTarget",
  "platformKnown", "platformIsLinux", "platformIsMac", "platformIsWindows",
  "running", "result", "exitCode", "progressLine", "filesProcessed", "outputSize",
  "updateDismissed", "updateChecked", "updateAvailable", "latestTag", "updateCopied",
  "picker_exit", "picker_err",
  "pack_line", "pack_exit", "pack_err",
  "stat_exit", "stat_err",
  "open_url_exit", "open_url_err",
  "uname_exit", "uname_err",
  "check_update", "update_fetched", "update_fetch_err",
  "clear_result",
  "quit",
] as const;

function freshEditor(): TextEditState {
  return { text: new Uint8Array(0), selection: { anchor: 0, focus: 0 }, composition: null };
}

export function initialModel(): [Model, Cmd<Msg>] {
  const model: Model = {
    platformKnown: false,
    platformIsLinux: false,
    platformIsMac: false,
    platformIsWindows: false,

    inputEditor: freshEditor(),
    outputEditor: freshEditor(),

    pngRecompress: true,
    pngStripMeta: true,
    pngLossy: true,
    pngProtectAlpha: true,
    ogg: true,
    oggStripComments: true,
    jsonMinify: true,
    textMinify: true,

pickerTarget: "none",

    running: false,
    result: new Uint8Array(0),
    exitCode: 0,
    hadResult: false,
    progressLine: new Uint8Array(0),
    filesProcessed: 0,
    outputSize: 0,

    updatePending: false,
    updateDismissed: false,
    updateChecked: false,
    updateAvailable: false,
    latestTag: new Uint8Array(0),
    updateCopied: false,

    optimizationOpen: false,
  };
  // Boot: detect the host (uname) and fetch the release feed in parallel.
  return [
    model,
    Cmd.batch<Msg>([
      Cmd.spawn([asciiBytes("uname"), asciiBytes("-s")], {
        key: "uname",
        collect: true,
        exit: "uname_exit",
        err: "uname_err",
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

function trimText(text: Bytes): Bytes {
  return text.trim();
}

function trimEndNewline(text: Bytes): Bytes {
  let end = text.length;
  while (end > 0) {
    const b = text[end - 1];
    if (b === 0x0a || b === 0x0d || b === 0x20 || b === 0x09) {
      end -= 1;
    } else {
      break;
    }
  }
  let start = 0;
  while (start < end) {
    const b = text[start];
    if (b === 0x0a || b === 0x0d || b === 0x20 || b === 0x09) {
      start += 1;
    } else {
      break;
    }
  }
  return text.subarray(start, end);
}

// The last non-empty line of `text` (LF-separated). compack prints per-file
// log lines then a single "done: ..." summary on success, or a "compack:
// <err>" line on failure — the last line is the one worth showing.
function lastLine(text: Bytes): Bytes {
  let end = text.length;
  while (end > 0 && (text[end - 1] === 0x0a || text[end - 1] === 0x0d)) end -= 1;
  if (end === 0) return new Uint8Array(0);
  let start = end;
  while (start > 0 && text[start - 1] !== 0x0a) start -= 1;
  // trim trailing whitespace and a possible leading prefix of the line
  let s = start;
  let e = end;
  while (e > s) {
    const b = text[e - 1];
    if (b === 0x0d || b === 0x20 || b === 0x09) e -= 1; else break;
  }
  while (s < e) {
    const b = text[s];
    if (b === 0x0d || b === 0x20 || b === 0x09) s += 1; else break;
  }
  return text.subarray(s, e);
}

// Is `text` all ASCII digits?
function isDigits(text: Bytes): boolean {
  if (text.length === 0) return false;
  for (let i = 0; i < text.length; i++) {
    const b = text[i];
    if (b < 0x30 || b > 0x39) return false;
  }
  return true;
}

function parseDigits(text: Bytes): number {
  let v = 0;
  for (let i = 0; i < text.length; i++) {
    v = v * 10 + (text[i] - 0x30);
  }
  return v;
}

function inputPath(model: Model): Bytes {
  return trimText(model.inputEditor.text);
}

// computeOutPath builds the `-out` value passed to compack: the chosen output
// folder joined with the input pack's basename plus the `-compack.zip` suffix.
// compack itself writes and renames a temp file to this path. When the output
// folder is empty the name is emitted relative to the working directory.
function computeOutPath(model: Model): Bytes {
  const dir = trimText(model.outputEditor.text);
  const inBase = basename(inputPath(model));
  let name = stripZipExt(inBase);
  if (name.length === 0) name = asciiBytes("pack");
  const suffix = asciiBytes("-compack.zip");
  if (dir.length === 0) {
    return concatBytes([name, suffix]);
  }
  const dirT = trimTrailingSep(dir);
  const sep = model.platformIsWindows ? asciiBytes("\\") : asciiBytes("/");
  return concatBytes([dirT, sep, name, suffix]);
}

// Strip a single trailing path separator ( '/' or '\' ).
function trimTrailingSep(text: Bytes): Bytes {
  let end = text.length;
  while (end > 0) {
    const b = text[end - 1];
    if (b === 0x2f || b === 0x5c) end -= 1; else break;
  }
  return text.subarray(0, end);
}

// The last path segment of `path` (the basename), after stripping any trailing
// separators. The extension is kept intact.
function basename(path: Bytes): Bytes {
  const t = trimTrailingSep(path);
  if (t.length === 0) return new Uint8Array(0);
  let start = t.length - 1;
  while (start > 0) {
    const b = t[start - 1];
    if (b === 0x2f || b === 0x5c) break;
    start -= 1;
  }
  return t.subarray(start);
}

// Case-insensitive ".zip" extension check (ASCII lowercase -> uppercase compare).
function endsWithZip(text: Bytes): boolean {
  const n = text.length;
  if (n < 5) return false; // need at least "a.zip"
  if (text[n - 4] !== 0x2e) return false;
  return up(text[n - 3]) === 0x5a && up(text[n - 2]) === 0x49 && up(text[n - 1]) === 0x50;
}

function up(b: number): number {
  return b >= 0x61 && b <= 0x7a ? b - 0x20 : b;
}

// Remove a single trailing ".zip" extension (case-insensitive).
function stripZipExt(name: Bytes): Bytes {
  return endsWithZip(name) ? name.subarray(0, name.length - 4) : name;
}

function concatBytes(parts: Bytes[]): Bytes {
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

// Strip a single leading ASCII 'v' (97 -> won't match; v is 0x76).
function stripLeadingV(text: Bytes): Bytes {
  if (text.length > 0 && text[0] === 0x76) return text.subarray(1);
  return text;
}

// Parse a "major.minor.patch" bytes value into three integers. Missing parts
// count as zero.
interface Semver { readonly ok: boolean; readonly major: number; readonly minor: number; readonly patch: number; }

function parseSemver(text: Bytes): Semver {
  let major = 0;
  let minor = 0;
  let patch = 0;
  let part = 0;
  let any = false;
  for (let i = 0; i <= text.length; i++) {
    if (i === text.length || text[i] === 0x2e) {
      if (!any) {
        // empty part -> treat as zero, but the whole parse still ok only if
        // we saw at least one digit somewhere.
      }
      if (part === 0) major = major;
      if (part === 1) minor = minor;
      if (part === 2) patch = patch;
      part += 1;
      any = false;
      continue;
    }
    const b = text[i];
    if (b < 0x30 || b > 0x39) {
      return { ok: false, major: 0, minor: 0, patch: 0 };
    }
    any = true;
    const d = b - 0x30;
    if (part === 0) major = major * 10 + d;
    else if (part === 1) minor = minor * 10 + d;
    else patch = patch * 10 + d;
  }
  const saw = text.length > 0;
  return { ok: saw, major: major, minor: minor, patch: patch };
}

// A newer remote > current comparison (strict).
function isNewer(remote: Semver, current: Semver): boolean {
  if (remote.major !== current.major) return remote.major > current.major;
  if (remote.minor !== current.minor) return remote.minor > current.minor;
  return remote.patch > current.patch;
}

// Extract the "tag_name":"vX.Y.Z" value from a GitHub release JSON body.
function extractTag(body: Bytes): Bytes {
  const key = asciiBytes('"tag_name"');
  let i = 0;
  while (i + key.length <= body.length) {
    let match = true;
    for (let j = 0; j < key.length; j++) {
      if (body[i + j] !== key[j]) { match = false; break; }
    }
    if (match) { i += key.length; break; }
    i += 1;
  }
  if (i + key.length > body.length) return new Uint8Array(0);
  // skip up to the opening quote of the value
  let p = i;
  while (p < body.length) {
    if (body[p] === 0x22) break;
    p += 1;
    if (p - i > 32) return new Uint8Array(0);
  }
  if (p >= body.length) return new Uint8Array(0);
  p += 1;
  const start = p;
  while (p < body.length && body[p] !== 0x22) p += 1;
  if (p >= body.length) return new Uint8Array(0);
  return body.subarray(start, p);
}

function flagBool(name: string, value: boolean): Bytes {
  if (name === "png-recompress") return value ? asciiBytes("-png-recompress=true") : asciiBytes("-png-recompress=false");
  if (name === "png-strip-meta") return value ? asciiBytes("-png-strip-meta=true") : asciiBytes("-png-strip-meta=false");
  if (name === "png-lossy") return value ? asciiBytes("-png-lossy=true") : asciiBytes("-png-lossy=false");
  if (name === "png-protect-alpha") return value ? asciiBytes("-png-protect-alpha=true") : asciiBytes("-png-protect-alpha=false");
  if (name === "ogg") return value ? asciiBytes("-ogg=true") : asciiBytes("-ogg=false");
  if (name === "ogg-strip-comments") return value ? asciiBytes("-ogg-strip-comments=true") : asciiBytes("-ogg-strip-comments=false");
  if (name === "json-minify") return value ? asciiBytes("-json-minify=true") : asciiBytes("-json-minify=false");
  return value ? asciiBytes("-text-minify=true") : asciiBytes("-text-minify=false");
}

function canPack(model: Model): boolean {
  if (model.running) return false;
  if (trimText(model.inputEditor.text).length === 0) return false;
  return true;
}

// ---------------------------------------------------------------------------
// Update

export function update(model: Model, msg: Msg): Model | [Model, Cmd<Msg>] {
  switch (msg.kind) {
    case "edit_input": {
      const next = applyTextInputEvent(model.inputEditor, msg.edit, TEXT_CAP);
      if (next === null) return model;
      return { ...model, inputEditor: next };
    }
    case "edit_output": {
      const next = applyTextInputEvent(model.outputEditor, msg.edit, TEXT_CAP);
      if (next === null) return model;
      return { ...model, outputEditor: next };
    }

    case "browse_input_folder": {
      if (model.running) return model;
      const start: Model = { ...model, pickerTarget: "input_folder" as PickerTarget, updateCopied: false };
      if (model.platformIsLinux) {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--directory"), asciiBytes("--title=Select resource pack folder")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsMac) {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose folder with prompt "Select resource pack folder")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsWindows) {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description='Select resource pack folder'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.SelectedPath)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "browse_input_file": {
      if (model.running) return model;
      const start: Model = { ...model, pickerTarget: "input_file" as PickerTarget, updateCopied: false };
      if (model.platformIsLinux) {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--file-filter=Zip files | *.zip"), asciiBytes("--file-filter=All files | *"), asciiBytes("--title=Select resource pack .zip")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsMac) {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose file with prompt "Select resource pack .zip")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsWindows) {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.OpenFileDialog; $f.Filter='Zip files (*.zip)|*.zip|All files (*.*)|*.*'; $f.Title='Select resource pack .zip'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.FileName)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "browse_output": {
      if (model.running) return model;
      const start: Model = { ...model, pickerTarget: "output" as PickerTarget, updateCopied: false };
      if (model.platformIsLinux) {
        return [start, Cmd.spawn([asciiBytes("zenity"), asciiBytes("--file-selection"), asciiBytes("--directory"), asciiBytes("--title=Save pack to folder")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsMac) {
        return [start, Cmd.spawn([asciiBytes("osascript"), asciiBytes("-e"), asciiBytes('POSIX path of (choose folder with prompt "Save pack to folder")')], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      if (model.platformIsWindows) {
        return [start, Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), asciiBytes("Add-Type -AssemblyName System.Windows.Forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description='Save pack to folder'; if($f.ShowDialog() -eq 'OK'){[Console]::Out.Write($f.SelectedPath)}")], { key: "picker", collect: true, exit: "picker_exit", err: "picker_err" })];
      }
      return model;
    }
    case "picker_exit": {
      const picked = codeNotZero(msg.code) ? new Uint8Array(0) : trimEndNewline(msg.output);
      const target = model.pickerTarget;
      let nextModel: Model = { ...model, pickerTarget: "none", updateCopied: false };
      if (picked.length === 0) return nextModel;
      if (target === "input_folder" || target === "input_file") {
        nextModel = { ...nextModel, inputEditor: editorForText(picked) };
      } else if (target === "output") {
        nextModel = { ...nextModel, outputEditor: editorForText(picked) };
      }
      return nextModel;
    }
    case "picker_err": {
      return { ...model, pickerTarget: "none", result: msg.reason, exitCode: 1, hadResult: true };
    }

    case "toggle_png_recompress": return { ...model, pngRecompress: !model.pngRecompress };
    case "toggle_png_strip": return { ...model, pngStripMeta: !model.pngStripMeta };
    case "toggle_png_lossy": return { ...model, pngLossy: !model.pngLossy };
    case "toggle_png_protect_alpha": return { ...model, pngProtectAlpha: !model.pngProtectAlpha };
    case "toggle_ogg": return { ...model, ogg: !model.ogg };
    case "toggle_ogg_strip": return { ...model, oggStripComments: !model.oggStripComments };
    case "toggle_json": return { ...model, jsonMinify: !model.jsonMinify };
    case "toggle_text": return { ...model, textMinify: !model.textMinify };
    case "toggle_optimization": return { ...model, optimizationOpen: !model.optimizationOpen };

    case "pack": {
      if (!canPack(model)) return model;
      const bin = asciiBytes("assets/compack");
      const out = computeOutPath(model);
      const inP = inputPath(model);
      const start: Model = {
        ...model,
        running: true,
        result: new Uint8Array(0),
        exitCode: 0,
        hadResult: false,
        progressLine: new Uint8Array(0),
        filesProcessed: 0,
        updateCopied: false,
      };
      if (model.platformIsLinux || model.platformIsMac) {
        // `sh -c 'exec "$0" "$@" 2>&1'` merges stderr into the collected
        // stdout so the success summary line and any error text both land
        // in the captured output. Paths are separate argv elements, so
        // spaces and unicode never need shell quoting.
        return [
          start,
          Cmd.spawn(
            [
              asciiBytes("sh"), asciiBytes("-c"), SHELL_MERGE, bin,
              asciiBytes("-no-progress=true"),
              flagBool("png-recompress", model.pngRecompress),
              flagBool("png-strip-meta", model.pngStripMeta),
              flagBool("png-lossy", model.pngLossy),
              flagBool("png-protect-alpha", model.pngProtectAlpha),
              flagBool("ogg", model.ogg),
              flagBool("ogg-strip-comments", model.oggStripComments),
              flagBool("json-minify", model.jsonMinify),
              flagBool("text-minify", model.textMinify),
              asciiBytes("-out"), out, inP,
            ],
            { key: "pack", line: "pack_line", exit: "pack_exit", err: "pack_err" },
          ),
        ];
      }
      // windows: spawn compack directly (stderr stays out of the captured
      // output; the exit code and a generic message carry failure).
      return [
        start,
        Cmd.spawn(
          [
            bin,
            asciiBytes("-no-progress=true"),
            flagBool("png-recompress", model.pngRecompress),
            flagBool("png-strip-meta", model.pngStripMeta),
            flagBool("png-lossy", model.pngLossy),
            flagBool("png-protect-alpha", model.pngProtectAlpha),
            flagBool("ogg", model.ogg),
            flagBool("ogg-strip-comments", model.oggStripComments),
            flagBool("json-minify", model.jsonMinify),
            flagBool("text-minify", model.textMinify),
            asciiBytes("-out"), out, inP,
          ],
          { key: "pack", line: "pack_line", exit: "pack_exit", err: "pack_err" },
        ),
      ];
    }
    case "cancel_pack": {
      if (!model.running) return model;
      return [model, Cmd.cancel("pack")];
    }
    case "pack_line": {
      const line = trimEndNewline(msg.line);
      if (line.length === 0) return model;
      let count = model.filesProcessed;
      if (startsWithText(line, asciiBytes("[")) || startsWithText(line, asciiBytes("optimizing"))) {
        count += 1;
      }
      return { ...model, progressLine: line, filesProcessed: count };
    }
    case "pack_exit": {
      const lastProgress = model.progressLine;
      let done = lastProgress;
      if (done.length === 0) {
        done = msg.code === 0
          ? asciiBytes("compack finished")
          : asciiBytes(`compack exited with code ${msg.code}`);
      }
      if (msg.code === 0) {
        const outPath = computeOutPath(model);
        if (model.platformIsLinux) {
          return [
            { ...model, running: false, result: done, exitCode: msg.code, hadResult: true },
            Cmd.spawn([asciiBytes("stat"), asciiBytes("-c"), asciiBytes("%s"), outPath], {
              key: "stat",
              collect: true,
              exit: "stat_exit",
              err: "stat_err",
            }),
          ];
        }
        if (model.platformIsMac) {
          return [
            { ...model, running: false, result: done, exitCode: msg.code, hadResult: true },
            Cmd.spawn([asciiBytes("stat"), asciiBytes("-f"), asciiBytes("%z"), outPath], {
              key: "stat",
              collect: true,
              exit: "stat_exit",
              err: "stat_err",
            }),
          ];
        }
        if (model.platformIsWindows) {
          return [
            { ...model, running: false, result: done, exitCode: msg.code, hadResult: true },
            Cmd.spawn([asciiBytes("powershell"), asciiBytes("-NoProfile"), asciiBytes("-Command"), concatBytes([asciiBytes("(Get-Item '"), outPath, asciiBytes("')).Length")])], {
              key: "stat",
              collect: true,
              exit: "stat_exit",
              err: "stat_err",
            }),
          ];
        }
      }
      return {
        ...model,
        running: false,
        result: done,
        exitCode: msg.code,
        hadResult: true,
      };
    }
    case "pack_err": {
      const reason = msg.reason;
      const text: Bytes = reason.length > 0 ? reason : asciiBytes("compack could not run");
      return {
        ...model,
        running: false,
        result: text,
        exitCode: 1,
        hadResult: true,
      };
    }
    case "stat_exit": {
      const sizeStr = trimEndNewline(msg.output);
      let size = 0;
      if (isDigits(sizeStr)) {
        size = parseDigits(sizeStr);
      }
      return { ...model, outputSize: size };
    }
    case "stat_err": {
      return model;
    }
    case "clear_result": {
      return { ...model, hadResult: false, result: new Uint8Array(0), exitCode: 0 };
    }

    case "uname_exit": {
      const name = trimEndNewline(msg.output);
      let nextModel: Model = { ...model, platformKnown: true };
      if (startsWithText(name, asciiBytes("Darwin"))) {
        nextModel = { ...nextModel, platformIsMac: true };
      } else if (startsWithText(name, asciiBytes("Linux"))) {
        nextModel = { ...nextModel, platformIsLinux: true };
      } else {
        nextModel = { ...nextModel, platformIsWindows: true };
      }
      return nextModel;
    }
    case "uname_err": {
      // No `uname` — assume windows.
      return { ...model, platformKnown: true, platformIsWindows: true };
    }

    case "check_update": {
      return [
        { ...model, updatePending: true, updateCopied: false },
        Cmd.fetch(
          { url: asciiBytes("https://api.github.com/repos/qore-games/compack/releases/latest"), method: "GET", headers: { "Accept": asciiBytes("application/vnd.github+json"), "User-Agent": asciiBytes("compack-app") } },
          { key: "release", ok: "update_fetched", err: "update_fetch_err" },
        ),
      ];
    }
    case "update_fetched": {
      const tag = extractTag(msg.body);
      if (tag.length === 0) {
        return { ...model, updatePending: false, updateChecked: true };
      }
      const remote = parseSemver(stripLeadingV(tag));
      const current = parseSemver(stripLeadingV(CURRENT_VERSION));
      const available = remote.ok && current.ok && isNewer(remote, current);
      return {
        ...model,
        updatePending: false,
        updateChecked: true,
        updateAvailable: available,
        latestTag: tag,
      };
    }
    case "update_fetch_err": {
      return { ...model, updatePending: false, updateChecked: true };
    }
    case "dismiss_update": {
      return { ...model, updateDismissed: true };
    }
    case "copy_update_link": {
      return [model, Cmd.clipboardWrite(RELEASE_PAGE)];
    }
    case "open_github": {
      const url = asciiBytes("https://github.com/qore-games/compack");
      if (model.platformIsLinux) {
        return [model, Cmd.spawn([asciiBytes("xdg-open"), url], { key: "open_url", exit: "open_url_exit", err: "open_url_err" })];
      }
      if (model.platformIsMac) {
        return [model, Cmd.spawn([asciiBytes("open"), url], { key: "open_url", exit: "open_url_exit", err: "open_url_err" })];
      }
      if (model.platformIsWindows) {
        return [model, Cmd.spawn([asciiBytes("cmd"), asciiBytes("/c"), asciiBytes("start"), url], { key: "open_url", exit: "open_url_exit", err: "open_url_err" })];
      }
      return model;
    }
    case "open_url_exit": {
      return model;
    }
    case "open_url_err": {
      return model;
    }
    case "quit": {
      return [model, Cmd.quitApp()];
    }
  }
}

function editorForText(text: Bytes): TextEditState {
  return { text: text, selection: { anchor: text.length, focus: text.length }, composition: null };
}

function codeNotZero(code: number): boolean {
  return code !== 0;
}

function startsWithText(haystack: Bytes, needle: Bytes): boolean {
  if (haystack.length < needle.length) return false;
  for (let i = 0; i < needle.length; i++) {
    if (haystack[i] !== needle[i]) return false;
  }
  return true;
}

// ---------------------------------------------------------------------------
// Derived values the markup binds

export function canPackNow(model: Model): boolean {
  return canPack(model);
}

export function isPacking(model: Model): boolean {
  return model.running;
}

export function updateBannerVisible(model: Model): boolean {
  return model.updateAvailable && !model.updateDismissed;
}

export function latestVersionText(model: Model): Bytes {
  return model.latestTag;
}

export function currentVersionText(model: Model): Bytes {
  return CURRENT_VERSION;
}

export function statusText(model: Model): Bytes {
  if (model.running) return asciiBytes("Packing…");
  if (model.hadResult) {
    if (model.exitCode === 0) return asciiBytes("Done — ");
    return asciiBytes("Failed — ");
  }
  return asciiBytes("");
}

export function resultText(model: Model): Bytes {
  return model.result;
}

export function progressText(model: Model): Bytes {
  if (!model.running) return new Uint8Array(0);
  if (model.progressLine.length > 0) return model.progressLine;
  return asciiBytes("Starting...");
}

export function filesProcessedText(model: Model): Bytes {
  if (!model.running || model.filesProcessed === 0) return new Uint8Array(0);
  return asciiBytes(`${model.filesProcessed} files`);
}

export function outputSizeText(model: Model): Bytes {
  if (model.outputSize === 0) return new Uint8Array(0);
  const size = model.outputSize;
  if (size < 1024) {
    return asciiBytes(`${size} B`);
  } else if (size < 1024 * 1024) {
    const kb = size >> 10;
    return asciiBytes(`${kb} KB`);
  } else {
    const mb = size >> 20;
    return asciiBytes(`${mb} MB`);
  }
}

export function platformLabel(model: Model): Bytes {
  if (model.platformIsLinux) return asciiBytes("linux");
  if (model.platformIsMac) return asciiBytes("macOS");
  if (model.platformIsWindows) return asciiBytes("windows");
  return asciiBytes("");
}

export function packButtonText(model: Model): Bytes {
  return model.running ? asciiBytes("Stop") : asciiBytes("Pack");
}
