# Changelog

## 1.1.0

On the Minebox pack (62k files, 32 threads): 20.4 s → 13.3 s, 70.9 MB → 68.8 MB,
565 MB → 340 MB of memory. File contents are unchanged.

**Changed**

- Local file headers no longer repeat file names (−2 MB on Minebox). Minecraft
  and `unzip` read the packs; 7-Zip no longer opens them, `-zip-local-names`
  restores the names.
- Entries are stored as they are when deflating would make them bigger.
- `-skip-png` now turns off the lossy pngquant pass too (it only turned off
  oxipng).
- The output is readable by everyone (`0644`), not only its owner.
- Log lines have no timestamp; the summary compares the input's size on disk
  with the written ZIP and prints the output's SHA-1.
- `-threads 0` means one per CPU, as in `config.yml`.
- A second input path is an error instead of being ignored.

**Added**

- `-events`: progress, notes and the summary as JSON lines on stdout.
- `-cache <dir>`: reuse PNG/OGG results between runs (off by default).
- `-ogg-lossy` / `-ogg-quality`: opt-in Vorbis re-encode (channels kept,
  rates above 48 kHz halved), kept when at least 15% smaller: −5.6 MB (−8%) on
  Minebox. The encoder, oggshrink, is built from `tools/oggshrink`.
- `-zip-local-names`, `-version`.
- Tool failures (pngquant, oxipng, OptiVorbis, oggshrink) are reported as notes.

**Fixed**

- Each run left the extracted tools in the temp folder.
- An interrupted or killed run left a temp ZIP next to the output; the next run
  removes it.
- pngquant failed when both quality bounds were 0.

**Desktop app**

- Redesigned: a wider two-column window, presets (Lossless, Balanced,
  Smallest) next to every option, a progress bar, then a result card (size
  saved, time, SHA-1) with show in folder and copy buttons.
- The output folder defaults to the pack's folder; paths and options are
  remembered.
- `kdialog` is used when `zenity` is missing.
- The app finds its bundled CLI in packaged builds (macOS `.app`, Linux archive,
  AppImage); the Windows zip no longer overwrites the app with the CLI.
- New logo.
