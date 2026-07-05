// Command compack optimizes Minecraft resource and data packs in place into a
// single ready-to-use ZIP file. It is a fast, dependency-light Go alternative
// to PackSquash targeting the optimizations that matter most for in-game
// load times and distribution size:
//
//   - JSON / JSONC / .mcmeta: minify (strip comments + whitespace)
//   - PNG: 100% lossless rewriter via embedded oxipng (drop ancillary chunks + recompress IDAT)
//   - Ogg (.ogg / .oga): lossless repackaging via embedded OptiVorbis (metadata stripped)
//   - GLSL (.vsh/.fsh/.glsl), .lang, .properties: strip comments + blanks
//
// The oxipng and OptiVorbis binaries are embedded inside compack for Linux /
// macOS / Windows on amd64 + arm64 (windows/arm64 excepted — no upstream
// release) so no external binaries need to be on $PATH. A live progress bar
// is drawn on stderr while files are being optimized.
//
// Everything else is passed through unchanged and stored with default ZIP
// DEFLATE compression (klauspost/compress/flate, which yields a smaller
// payload than the standard library at the same level). The output ZIP itself
// is written in PackSquash-disregard mode: minimal headers, dummy timestamps +
// no data descriptors + deduplication of identical files, all of which are
// byte-for-byte valid for Minecraft's java.util.zip.ZipFile reader but may trip
// up strict cross-tool ZIP readers.
package main

import (
	"archive/zip"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qore-games/compack/optim"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// version is overwritten at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `compack - fast Minecraft resource/data pack optimizer

Usage:
  compack [-flags...] <input-dir> [-out output.zip] [-config config.yml]

Examples:
  compack ./my-resourcepack -out pack.zip
  compack -png-strip-meta -ogg -json-minify ./my-pack
  compack -skip-png -skip-ogg -json-minify ./my-pack        # only minify text/json
  compack -config my-config.yml ./my-pack                  # use settings from config file

Flags:`

// config mirrors every user-facing setting. It is populated from (in order):
// defaults -> config.yml (if found / specified) -> explicit command-line flags.
type config struct {
	inDir      string
	outZip     string
	threads    int
	quiet      bool
	verbose    bool
	dryRun     bool
	noProgress bool

	optim.Options
}

// yamlConfig is the on-disk representation of compack's settings. Field names
// use snake_case keys via yaml struct tags; absent keys keep their go zero
// value (and are then overlaid on top of the built-in defaults using
// applyYAML, so an unset entry does not stomp a default).
type yamlConfig struct {
	Out        string   `yaml:"out"`
	Threads    int      `yaml:"threads"`
	Quiet      bool     `yaml:"quiet"`
	Verbose    bool     `yaml:"verbose"`
	DryRun     bool     `yaml:"dry_run"`
	NoProgress bool     `yaml:"no_progress"`
	JSON       yamlJSON `yaml:"json"`
	PNG        yamlPNG  `yaml:"png"`
	OGG        yamlOGG  `yaml:"ogg"`
	Text       yamlText `yaml:"text"`
}

type yamlJSON struct {
	Minify *bool `yaml:"minify"`
}

type yamlPNG struct {
	Recompress    *bool `yaml:"recompress"`
	StripMeta     *bool `yaml:"strip_meta"`
	Level         *int  `yaml:"level"`
	KeepColorProf *bool `yaml:"keep_color_profile"`
	LossyQuant    *bool `yaml:"lossy_quant"`
	QuantMin      *int  `yaml:"quant_min"`
	QuantMax      *int  `yaml:"quant_max"`
}

type yamlOGG struct {
	Optimize      *bool `yaml:"optimize"`
	StripComments *bool `yaml:"strip_comments"`
}

type yamlText struct {
	Minify *bool `yaml:"minify"`
}

// defaultConfig returns the built-in default settings used when neither a
// config file nor flags override them. These values match the historical
// defaults from before config.yml support was added.
func defaultConfig() config {
	return config{
		outZip:  "pack.zip",
		threads: runtime.NumCPU(),
		Options: optim.Options{
			JSONMinify:       true,
			PNGRecompress:    true,
			PNGStripMeta:     true,
			PNGLevel:         0,
			PNGLossyQuant:    true,
			PNGQuantMin:      65,
			PNGQuantMax:      90,
			OGGOptimize:      true,
			OGGStripComments: true,
			TextMinify:       true,
		},
	}
}

// applyYAML overlays the parsed YAML document on top of cfg. Only keys that
// were present in the file (non-nil pointers) are applied, so an absent key
// in config.yml never overrides the default value.
func (cfg *config) applyYAML(y yamlConfig) {
	if y.Out != "" {
		cfg.outZip = y.Out
	}
	if y.Threads > 0 {
		cfg.threads = y.Threads
	}
	cfg.quiet = cfg.quiet || y.Quiet
	cfg.verbose = cfg.verbose || y.Verbose
	cfg.dryRun = cfg.dryRun || y.DryRun
	cfg.noProgress = cfg.noProgress || y.NoProgress

	if y.JSON.Minify != nil {
		cfg.JSONMinify = *y.JSON.Minify
	}
	if y.PNG.Recompress != nil {
		cfg.PNGRecompress = *y.PNG.Recompress
	}
	if y.PNG.StripMeta != nil {
		cfg.PNGStripMeta = *y.PNG.StripMeta
	}
	if y.PNG.Level != nil {
		cfg.PNGLevel = *y.PNG.Level
	}
	if y.PNG.LossyQuant != nil {
		cfg.PNGLossyQuant = *y.PNG.LossyQuant
	}
	if y.PNG.QuantMin != nil {
		cfg.PNGQuantMin = *y.PNG.QuantMin
	}
	if y.PNG.QuantMax != nil {
		cfg.PNGQuantMax = *y.PNG.QuantMax
	}
	if y.PNG.KeepColorProf != nil {
		cfg.Options.PNGKeepColorMng = *y.PNG.KeepColorProf
	}
	if y.OGG.Optimize != nil {
		cfg.OGGOptimize = *y.OGG.Optimize
	}
	if y.OGG.StripComments != nil {
		cfg.OGGStripComments = *y.OGG.StripComments
	}
	if y.Text.Minify != nil {
		cfg.TextMinify = *y.Text.Minify
	}
}

// loadConfigYAML reads and parses the YAML config file at path. A missing path
// is a fatal error so users get a clear message when they typo -config.
func loadConfigYAML(path string) (yamlConfig, error) {
	var y yamlConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return y, err
	}
	if err := yaml.Unmarshal(data, &y); err != nil {
		return y, fmt.Errorf("parse YAML: %w", err)
	}
	return y, nil
}

// scanConfigFlag peeks at argv to find a -config / --config value before the
// main flag parser runs. This lets us load the YAML file first so its values
// can be used as the defaults for the flag parser, meaning any explicit flag
// passed on the command line overrides the YAML value, and un-set flags fall
// back to the YAML value. Returns ("", nil) when -config is absent.
func scanConfigFlag(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", nil
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			if name[:eq] != "config" {
				continue
			}
			return name[eq+1:], nil
		}
		if name != "config" {
			continue
		}
		if i+1 >= len(args) {
			return "", errors.New("-config requires a path argument")
		}
		return args[i+1], nil
	}
	return "", nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "compack:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := defaultConfig()

	// Optional config file precedence:
	//   1. -config <path>  -> load exactly that path (error if it cannot be read)
	//   2. ./config.yml    -> auto-loaded if present in the working directory
	//   3. built-in defaults -> otherwise
	// The YAML values become the new defaults for the flag parser below, so any
	// flag passed explicitly on the command line takes precedence over YAML and
	// omitted flags fall back to the YAML value.
	configPath, err := scanConfigFlag(args)
	if err != nil {
		return err
	}
	if configPath == "" {
		if _, statErr := os.Stat("config.yml"); statErr == nil {
			configPath = "config.yml"
		}
	}
	if configPath != "" {
		y, err := loadConfigYAML(configPath)
		if err != nil {
			return fmt.Errorf("load config %q: %w", configPath, err)
		}
		cfg.applyYAML(y)
	}

	fs := flag.NewFlagSet("compack", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		fs.PrintDefaults()
	}

	// Allow flags and positional arguments in any order by hoisting every
	// non-flag token to the end of argv before calling fs.Parse. This avoids
	// the surprise where `compack ./pack -out pack.zip` silently drops -out.
	args = reorderArgs(args)

	var (
		skipPNG, skipOGG, skipJSON, skipText bool
		skipPNGQuant                         bool
		pngKeepColorMng                      bool
	)
	// -config is declared for --help / usage readability; its value was
	// already consumed by scanConfigFlag above. We re-register it here so
	// that fs.Parse does not reject an unknown flag.
	var configFlag string
	fs.StringVar(&configFlag, "config", configPath, "path to a config.yml file with default settings (auto-loaded when present as ./config.yml)")
	fs.StringVar(&cfg.outZip, "out", cfg.outZip, "output ZIP file path")
	fs.IntVar(&cfg.threads, "threads", cfg.threads, "number of parallel file workers")
	fs.BoolVar(&cfg.quiet, "q", cfg.quiet, "suppress per-file log lines")
	fs.BoolVar(&cfg.verbose, "v", cfg.verbose, "verbose log (show file notes + skipped files)")
	fs.BoolVar(&cfg.dryRun, "dry-run", cfg.dryRun, "walk and optimize but do not write the ZIP")
	fs.BoolVar(&cfg.noProgress, "no-progress", cfg.noProgress, "never show the progress bar (default: auto when stderr is a TTY)")

	fs.BoolVar(&cfg.JSONMinify, "json-minify", cfg.JSONMinify, "minify JSON/JSONC/.mcmeta files")
	fs.BoolVar(&skipJSON, "skip-json", false, "do not minify JSON")

	fs.BoolVar(&cfg.PNGRecompress, "png-recompress", cfg.PNGRecompress, "recompress PNG IDAT with strongest zlib level")
	fs.BoolVar(&cfg.PNGStripMeta, "png-strip-meta", cfg.PNGStripMeta, "drop ancillary PNG chunks (metadata removal)")
	fs.BoolVar(&skipPNG, "skip-png", false, "do not optimize PNGs (alias for -png-recompress=false -png-strip-meta=false)")
	fs.IntVar(&cfg.PNGLevel, "png-level", cfg.PNGLevel, "oxipng optimization preset (1-6, 0 = best-effort default 4, >6 = max)")
	fs.BoolVar(&pngKeepColorMng, "png-keep-color-profile", false, "keep gAMA/iCCP/cHRM/sRGB chunks")

	fs.BoolVar(&cfg.PNGLossyQuant, "png-lossy", cfg.PNGLossyQuant, "lossily remap RGB/RGBA PNGs to an 8-bit palette via embedded pngquant (libimagequant) before the lossless oxipng pass; matches PackSquash's compression level")
	fs.BoolVar(&skipPNGQuant, "skip-png-quant", false, "do not run the lossy pngquant palette step (alias for -png-lossy=false)")
	fs.IntVar(&cfg.PNGQuantMin, "png-quant-min", cfg.PNGQuantMin, "pngquant --quality lower bound (0-100); below this quality the original pixels are kept")
	fs.IntVar(&cfg.PNGQuantMax, "png-quant-max", cfg.PNGQuantMax, "pngquant --quality upper bound (0-100); trades palette size vs quality")

	fs.BoolVar(&cfg.OGGOptimize, "ogg", cfg.OGGOptimize, "losslessly shrink OGG/Ogg Vorbis via OptiVorbis (use -skip-ogg to disable)")
	fs.BoolVar(&skipOGG, "skip-ogg", false, "do not optimize OGGs (alias for -ogg=false)")
	fs.BoolVar(&cfg.OGGStripComments, "ogg-strip-comments", cfg.OGGStripComments, "remove OGG comment fields (artist/title/etc.); set -ogg-strip-comments=false to keep")

	fs.BoolVar(&cfg.TextMinify, "text-minify", cfg.TextMinify, "strip comments + blank lines from shaders/lang/properties")
	fs.BoolVar(&skipText, "skip-text", false, "do not minify shaders/lang/properties")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if skipJSON {
		cfg.JSONMinify = false
	}
	if skipPNG {
		cfg.PNGRecompress = false
		cfg.PNGStripMeta = false
	}
	if skipPNGQuant {
		cfg.PNGLossyQuant = false
	}
	if skipOGG {
		cfg.OGGOptimize = false
	}
	if skipText {
		cfg.TextMinify = false
	}
	// png-keep-color-profile is an explicit-set flag (default false). Only
	// respect it when set on the CLI; otherwise keep the YAML value already
	// applied above. fs.Visit reports only flags that were explicitly set.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "png-keep-color-profile" {
			cfg.Options.PNGKeepColorMng = pngKeepColorMng
		}
	})

	if fs.NArg() < 1 {
		fs.Usage()
		return errors.New("missing input directory")
	}
	cfg.inDir = fs.Arg(0)
	if cfg.threads < 1 {
		cfg.threads = 1
	}
	return build(cfg)
}

// fileSpec is one discovered resource pack file.
type fileSpec struct {
	rel  string // forward-slash relative path within the input directory
	abs  string
	size int64
}

// buildResult is the optimized form of one input file produced by the worker
// pool. We keep the bytes inline so the ZIP writer can stream them in any
// order we like without re-reading from disk.
type buildResult struct {
	rel     string
	orig    int64
	new     int64
	method  uint16
	changed bool
	data    []byte
	note    string
}

func build(cfg config) error {
	start := time.Now()
	info, err := os.Stat(cfg.inDir)
	if err != nil {
		return fmt.Errorf("stat input dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("input path %q is not a directory", cfg.inDir)
	}

	files, err := discover(cfg.inDir)
	if err != nil {
		return fmt.Errorf("walk input dir: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found under %q", cfg.inDir)
	}

	// Live progress bar drawn on stderr while files are optimized. Auto-detects
	// whether stderr is a TTY (so output captured to a file or piped through a
	// tool stays clean). Suppressed by -q, -v, -dry-run or -no-progress.
	showBar := !cfg.quiet && !cfg.verbose && !cfg.dryRun && !cfg.noProgress && term.IsTerminal(int(os.Stderr.Fd()))
	var bar *progressbar.ProgressBar
	if showBar {
		bar = progressbar.NewOptions(len(files),
			progressbar.OptionSetDescription("Optimizing pack"),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionShowCount(),
			progressbar.OptionShowIts(),
			progressbar.OptionSetItsString("files"),
			progressbar.OptionSetWidth(40),
			progressbar.OptionThrottle(60*time.Millisecond),
			progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
			progressbar.OptionShowDescriptionAtLineEnd(),
		)
	}

	// Optimize every file concurrently into a per-file in-memory buffer.
	// Peak memory equals the sum of all optimized file sizes (which is
	// always <= the sum of all original file sizes). For packs so huge that
	// this becomes a problem we'd spill to disk-backed buffers; in practice
	// packs rarely exceed a few hundred MB and a Go process can easily hold
	// that on a 64-bit machine.
	type rresult struct {
		buildResult
		err error
	}
	jobs := make(chan fileSpec)
	resultsCh := make(chan rresult, len(files))
	var wg sync.WaitGroup
	for w := 0; w < cfg.threads; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				data, err := os.ReadFile(f.abs)
				if err != nil {
					resultsCh <- rresult{buildResult: buildResult{rel: f.rel, orig: f.size}, err: err}
					continue
				}
				r, oerr := optim.Optimize(f.rel, data, cfg.Options)
				m := r.Method
				if m == 0 {
					m = zip.Deflate
				}
				resultsCh <- rresult{
					buildResult: buildResult{
						rel:     f.rel,
						orig:    int64(len(data)),
						new:     int64(len(r.Data)),
						method:  m,
						changed: r.Changed,
						data:    r.Data,
						note:    r.Note,
					},
					err: oerr,
				}
			}
		}()
	}
	go func() {
		for _, f := range files {
			jobs <- f
		}
		close(jobs)
	}()
	collected := make([]buildResult, 0, len(files))
	var firstErr error
	for i := 0; i < len(files); i++ {
		r := <-resultsCh
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		collected = append(collected, r.buildResult)
		if bar != nil {
			// Show the current file's optimizer type so the user can see at a
			// glance which task the worker pool is busy on right now.
			bar.Describe(fmt.Sprintf("%s %s", fileKind(r.rel), trimPath(r.rel)))
			bar.Add(1)
		}
	}
	wg.Wait()
	if bar != nil {
		_ = bar.Finish()
	}
	if firstErr != nil {
		return fmt.Errorf("optimizing files: %w", firstErr)
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].rel < collected[j].rel })

	// Statistics + log.
	var origTotal, newTotal int64
	var counts, changedFiles int
	var notes []string
	for _, r := range collected {
		origTotal += r.orig
		newTotal += r.new
		counts++
		if r.changed {
			changedFiles++
		}
		if r.note != "" {
			notes = append(notes, fmt.Sprintf("  %s: %s", r.rel, r.note))
		}
		if bar != nil {
			continue // the bar already shows progress; skip noisy per-file log
		}
		if cfg.verbose || (!cfg.quiet && r.changed) {
			sign := "="
			delta := r.orig - r.new
			if r.new < r.orig {
				sign = "-"
			} else if r.new > r.orig {
				sign = "+"
				delta = -delta
			}
			if cfg.verbose || sign == "-" {
				log.Printf("%-60s %s%s  (%d -> %d)", r.rel, sign, humanBytes(delta), r.orig, r.new)
			}
		}
	}
	for _, n := range notes {
		log.Println("note:", n)
	}

	if cfg.dryRun {
		log.Printf("dry run: %d files, would write %s", counts, cfg.outZip)
		return nil
	}

	if bar != nil {
		bar.Describe("Writing " + cfg.outZip)
	}
	tmp, dedupSaved, err := writeZip(collected, cfg.outZip)
	if err != nil {
		return fmt.Errorf("write zip: %w", err)
	}
	if err := os.Rename(tmp, cfg.outZip); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp zip: %w", err)
	}

	red := 0.0
	if origTotal > 0 {
		red = float64(origTotal-newTotal) / float64(origTotal) * 100
	}
	doneMsg := fmt.Sprintf("done: %d files, %d optimized, %s -> %s (%.1f%% smaller)", counts, changedFiles, humanBytes(origTotal), humanBytes(newTotal), red)
	if dedupSaved > 0 {
		doneMsg += fmt.Sprintf(", %s deduped", humanBytes(dedupSaved))
	}
	if bar != nil {
		doneMsg += " in " + time.Since(start).Round(time.Millisecond).String()
	}
	log.Print(doneMsg)
	return nil
}

// writeZip writes the pre-optimized bytes into a temporary ZIP file placed in
// the same directory as the destination, so the eventual rename(2) is atomic
// and does not cross filesystem boundaries. The output uses PackSquash's
// `disregard` conformance level: dummy timestamps, no data descriptors, no
// extra fields and deduplication of identical files. On success it returns the
// temp file path to rename into place plus the bytes saved by deduplication
// (so the caller can mention it in its summary log). On error, any temp file is
// removed before returning.
func writeZip(results []buildResult, outZip string) (string, int64, error) {
	return writeZipDisregard(results, outZip)
}

// discover returns the sorted list of regular files under root, with rel
// paths using forward slashes. Top-level hidden directories (starting with '.')
// are skipped so transient editor state (.git, .vscode, .idea) is not bundled.
func discover(root string) ([]fileSpec, error) {
	var out []fileSpec
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		rootAbs = root
	}
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(rootAbs, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := d.Name()
			if rel != "." && strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, fileSpec{rel: rel, abs: path, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// reorderArgs moves every non-flag token (and its standalone value when it
// looks like a positional rather than a flag value) to the end of the slice
// so that the stdlib flag package, which stops at the first non-flag, can
// still parse everything. We are conservative: a token is treated as a flag
// only if it starts with '-' (and isn't "-") or "--"; everything else is a
// positional. Boolean flags never consume the next token.
func reorderArgs(args []string) []string {
	var flags, positional []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "-" {
			positional = append(positional, a)
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			// Boolean flags are defined with fs.Bool / fs.BoolVar; the
			// flag package treats unknown bool flags as taking an explicit
			// "=value" only. Non-bool flags (String / Int / Float64) need
			// the next token *unless* it is itself a flag or there is none.
			// Since we cannot know the flag types ahead of time here, we
			// use a simple heuristic: if the next token does not start
			// with '-', attach it as the flag value.
			// Exception: '-h'/'--help'/'-h=true' style need no value.
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// Heuristic: only consume next arg if this flag is one of
				// the known value-flags. Otherwise it's likely a boolean.
				if takesValue(a) {
					flags = append(flags, args[i+1])
					i += 2
					continue
				}
			}
			i++
			continue
		}
		positional = append(positional, a)
		i++
	}
	return append(flags, positional...)
}

// takesValue reports whether the given flag token (e.g. "-out" or "--out")
// expects a separate value argument. Boolean flags do not. The set below must
// mirror the flag declarations in run().
func takesValue(flag string) bool {
	name := strings.TrimLeft(flag, "-")
	// Strip "=value" suffix if present.
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	switch name {
	case "out", "config", "threads", "png-level", "png-quant-min", "png-quant-max":
		return true
	default:
		return false
	}
}

func humanBytes(n int64) string {
	const (
		_  = iota
		KB = 1 << (10 * iota)
		MB
		GB
	)
	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/GB)
	case n >= MB:
		return fmt.Sprintf("%.2f MB", float64(n)/MB)
	case n >= KB:
		return fmt.Sprintf("%.2f KB", float64(n)/KB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// fileKind returns a short uppercase tag identifying which optimizer handles
// the file, or "PASS" when the file is passed through untouched. The tag is
// shown next to the progress bar so the user can see the current task at a
// glance.
func fileKind(rel string) string {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".png", ".apng":
		return "PNG"
	case ".ogg", ".oga":
		return "OGG"
	case ".json", ".jsonc", ".mcmeta", ".mcmetac":
		return "JSON"
	case ".vsh", ".fsh", ".glsl":
		return "GLSL"
	case ".lang":
		return "LANG"
	case ".properties":
		return "PROP"
	default:
		return "PASS"
	}
}

// trimPath shortens a long relative path so the progress-bar description stays
// on a single terminal line; keeps the trailing portion (which usually
// identifies the asset) and prefixes an ellipsis when truncated.
func trimPath(rel string) string {
	const max = 48
	if len(rel) <= max {
		return rel
	}
	return "..." + rel[len(rel)-(max-3):]
}
