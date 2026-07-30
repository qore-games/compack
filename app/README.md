# Compack

A small native desktop app for [compack](..) — the fast Minecraft: Java Edition
resource/data pack optimizer.

<img src="assets/icon.png" width="128" align="right" hspace="12" vspace="4">

Compack is a **native** app built with the
[Native SDK](https://native-sdk.dev) (`vercel-labs/native`).

Pick a resource pack (either an unpacked **folder** or a ready `.zip`),
pick an **output folder** and press **Pack**. compack writes a single
`<input name>-compack.zip` into the chosen output folder. The app bundles the
`compack` CLI binary in `assets/` (no need to have it on PATH)

## Requirements

| OS | Needs |
| --- | --- |
| macOS | `zenity` for the folder dialogs (or type/paste paths) |
| Linux | `zenity` for the folder dialogs (or type/paste paths) |
| Windows | Nothing extra (or type/paste paths) |

## Run / build

### Development

For development with automatic Go binary rebuilding:

```sh
# Linux / macOS
./dev.sh          # build Go binary + run app in dev mode (markup hot reload)

# Windows
dev.bat           # build Go binary + run app in dev mode
```

The `dev` scripts detect your platform, build the Go binary, and run `native dev` with hot reload. Every time you restart the app, the Go binary is rebuilt automatically.

### Build / Test

The `build` scripts compile the Go binary and run other native commands:

```sh
# Linux / macOS
./build.sh build    # build Go binary + build ReleaseFast app binary
./build.sh test     # build Go binary + run core test suite
./build.sh check    # build Go binary + validate core, markup, app.zon

# Windows
build.bat build     # build Go binary + build ReleaseFast app binary
build.bat test      # build Go binary + run core test suite
build.bat check     # build Go binary + validate core, markup, app.zon
```

The scripts detect your OS (linux/darwin/windows) and architecture (amd64/arm64), build the Go binary with the correct `GOOS`/`GOARCH`, place it in `assets/`, and then run the `native` command.
