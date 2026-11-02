# Configuration

Settings come from, in order: the built-in defaults, then a `config.yml` (the
one given with `-config`, else `./config.yml` when it exists), then the flags.
A later source overrides an earlier one; keys left out of the file keep their
default.

```sh
compack ./pack -out pack.zip -config compack.yml -png-quant-max 80
```

Flags and the input path can come in any order. There is exactly one input: a
pack folder or a `.zip`.

## config.yml

Every key, with its default:

```yaml
out: pack.zip          # output ZIP
threads: 0             # parallel workers, 0 = one per CPU
quiet: false           # no per-file lines (notes and the summary still print)
verbose: false         # a line for every file, changed or not
dry_run: false         # optimize, report the size, write nothing
no_progress: false     # no progress bar (it only shows on a terminal anyway)
events: false          # JSON lines on stdout, see below
cache: ""              # folder for cached PNG/OGG results, off when empty

zip:
  local_names: false   # repeat names in local headers, see "The ZIP" below

json:
  minify: true         # .json .jsonc .mcmeta .mcmetac: strip whitespace and comments

png:
  recompress: true          # oxipng: recompress IDAT
  strip_meta: true          # oxipng: drop ancillary chunks
  level: 0                  # oxipng preset 1-6, 0 = 4, above 6 = max
  keep_color_profile: false # keep gAMA/iCCP/cHRM/sRGB
  lossy_quant: true         # pngquant: 8-bit palette, lossy
  quant_min: 65             # pngquant quality floor (0-100): below it, keep the original pixels
  quant_max: 90             # pngquant quality target (0-100)
  protect_alpha: true       # no pngquant for RGBA PNGs with alpha between 0 and 255

ogg:
  optimize: true            # OptiVorbis, lossless
  strip_comments: true      # drop artist/title/... fields
  lossy: false              # re-encode first, see below (lossy)
  quality: 4                # Vorbis quality of those re-encodes, 0-10

text:
  minify: true              # .vsh .fsh .glsl .lang .properties: strip comments and blank lines
```

## Flags

| Flag | Key | Default |
| --- | --- | --- |
| `-out <file>` | `out` | `pack.zip` |
| `-config <file>` | | `./config.yml` if present |
| `-threads <n>` | `threads` | one per CPU |
| `-q` | `quiet` | off |
| `-v` | `verbose` | off |
| `-dry-run` | `dry_run` | off |
| `-no-progress` | `no_progress` | off |
| `-events` | `events` | off |
| `-cache <dir>` | `cache` | off |
| `-zip-local-names` | `zip.local_names` | off |
| `-json-minify` | `json.minify` | on |
| `-png-recompress` | `png.recompress` | on |
| `-png-strip-meta` | `png.strip_meta` | on |
| `-png-level <n>` | `png.level` | 0 (preset 4) |
| `-png-keep-color-profile` | `png.keep_color_profile` | off |
| `-png-lossy` | `png.lossy_quant` | on |
| `-png-quant-min <n>` | `png.quant_min` | 65 |
| `-png-quant-max <n>` | `png.quant_max` | 90 |
| `-png-protect-alpha` | `png.protect_alpha` | on |
| `-ogg` | `ogg.optimize` | on |
| `-ogg-strip-comments` | `ogg.strip_comments` | on |
| `-ogg-lossy` | `ogg.lossy` | off |
| `-ogg-quality <n>` | `ogg.quality` | 4 |
| `-text-minify` | `text.minify` | on |
| `-version` | | |

Boolean flags take `=false` to turn off (`-png-lossy=false`). Shortcuts:

| Flag | Same as |
| --- | --- |
| `-skip-png` | `-png-recompress=false -png-strip-meta=false -png-lossy=false` |
| `-skip-png-quant` | `-png-lossy=false` (lossless PNGs) |
| `-skip-ogg` | `-ogg=false` |
| `-skip-json` | `-json-minify=false` |
| `-skip-text` | `-text-minify=false` |

A later flag wins over a shortcut: `-skip-png -png-lossy` only runs pngquant.

## What each file goes through

| Files | Optimization |
| --- | --- |
| `.png` | pngquant palette (lossy, skipped for alpha-carrying RGBA), then oxipng (lossless). APNGs are left alone. The smallest of the original and the results is kept. |
| `.ogg` `.oga` | with `-ogg-lossy`, a re-encode first (below); then OptiVorbis: same samples, smaller container, no comments, fixed stream serials |
| `.json` `.jsonc` `.mcmeta` `.mcmetac` | whitespace and `//` `/* */` comments removed (a note when a strict `.json` had comments) |
| `.vsh` `.fsh` `.glsl` | comments removed, whitespace collapsed, blank lines dropped |
| `.lang` `.properties` | blank and comment lines dropped |
| anything else | kept as is |

Each entry is then deflated, or stored when deflating would not make it smaller.
Hidden folders (`.git`, `.idea`…) of a folder input are skipped, and so is the
output file when it is inside the input folder.

## -ogg-lossy

Off by default: it changes the audio. Every OGG is re-encoded at Vorbis
quality `-ogg-quality` (4 ≈ 128 kbps stereo), keeping its channels (stereo
stays stereo) and halving sample rates above 48 kHz. A re-encode replaces the
file only when it is at least 15% smaller, so files already encoded at about
that quality are not touched again.

| Minebox pack | Size | Time |
| --- | --- | --- |
| default | 68.8 MB | 14 s |
| `-ogg-lossy` (quality 4) | 63.2 MB | 15 s |
| `-ogg-lossy -ogg-quality 3` | 62.8 MB | 16 s |

The encoder is `oggshrink` (libvorbis), embedded like the other tools; its
source is in [tools/oggshrink](../tools/oggshrink).

## The ZIP

The ZIP keeps only what Minecraft reads (`java.util.zip.ZipFile`), like
PackSquash's `disregard` level:

- identical files share one body;
- local headers carry no file name (names live in the central directory);
- dummy timestamps, no data descriptors, no extra fields, no comments.

| Reader | Opens the pack |
| --- | --- |
| Minecraft (Java) | yes |
| `unzip` | yes, with `UNZIP_DISABLE_ZIPBOMB_DETECTION=TRUE` (shared bodies) and a warning per entry |
| 7-Zip | only with `-zip-local-names` (+2 MB on a 60k-file pack) |
| Python `zipfile` | no (shared bodies) |

The output is deterministic: the same input and settings give the same bytes,
so the SHA-1 printed at the end only changes when the pack does.

## -events

With `-events`, stdout gets one JSON object per line and nothing else
(errors are also printed on stderr):

```json
{"event":"start","total":62318}
{"event":"progress","done":120,"total":62318,"kind":"PNG","file":"assets/minecraft/textures/block/stone.png"}
{"event":"note","file":"assets/minecraft/models/item/x.json","note":"file contained comments despite a strict JSON extension; comments were stripped"}
{"event":"done","files":62318,"optimized":10322,"notes":1,"input_bytes":99627662,"output_bytes":68838138,"dedup_bytes":7048101,"ms":13100,"out":"/abs/path/pack.zip","sha1":"5b8f0d84…"}
{"event":"error","message":"stat input: stat ./pack: no such file or directory"}
```

`progress` comes at most every 100 ms (and once at the end). `kind` is `PNG`,
`OGG`, `JSON`, `GLSL`, `LANG`, `PROP` or `PASS`. `input_bytes` is the input
`.zip`, or the sum of a folder's files. A dry run has no `out` nor `sha1`.

## -cache

`-cache <dir>` keeps the result of every PNG and OGG by content. An entry is
reused only for the same bytes, settings and embedded tool versions, so a cache
can be shared between packs and branches. On the Minebox pack a warm run takes
0.6 s instead of 13 s. Nothing is ever deleted from it: point it at a folder
you can wipe (a CI cache, a build folder).

## Exit codes and interruption

`0` on success (and for `-h`), `1` on an error, `130` when interrupted. The ZIP
is written to `compack-<pid>-*.zip.tmp` next to the output and renamed at the
end, so the output is never half written. Temp files of a killed run are
removed by the next run.
