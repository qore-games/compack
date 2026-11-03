// Command compack optimizes Minecraft resource and data packs in place into a
// single ready-to-use ZIP file. It is a fast, dependency-light Go alternative
// to PackSquash targeting the optimizations that matter most for in-game
// load times and distribution size:
//
//   - JSON / JSONC / .mcmeta: minify (strip comments + whitespace)
//   - PNG: lossy palette pass via embedded pngquant, then lossless recompression
//     via embedded oxipng (drop ancillary chunks + recompress IDAT)
//   - Ogg (.ogg / .oga): lossless repackaging via embedded OptiVorbis (metadata
//     stripped), after an opt-in lossy re-encode via embedded oggshrink
//   - GLSL (.vsh/.fsh/.glsl), .lang, .properties: strip comments + blanks
//
// The tool binaries are embedded inside compack for Linux / macOS / Windows
// on amd64 + arm64 (windows/arm64 excepted — no upstream release) so no
// external binaries need to be on $PATH. A live progress bar is drawn on
// stderr while files are being optimized.
//
// Everything else is passed through unchanged and stored with ZIP DEFLATE
// (klauspost/compress/flate, which yields a smaller payload than the
// standard library at the same level), or stored as is when deflating would
// not make it smaller. The output ZIP itself is written in PackSquash-disregard
// mode (see zipwrite.go): byte-for-byte valid for Minecraft's
// java.util.zip.ZipFile reader but may trip up strict cross-tool ZIP readers.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klauspost/compress/flate"
	"github.com/qore-games/compack/optim"
	"gopkg.in/yaml.v3"
)

// version is overwritten at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `compack - fast Minecraft resource/data pack optimizer

Usage:
  compack [-flags...] <input> [-out output.zip] [-config config.yml]

  <input> is either a resource/data pack directory or a .zip archive.

Examples:
  compack ./my-resourcepack -out pack.zip
  compack ./my-resourcepack.zip -out pack.zip
  compack -skip-png-quant ./my-pack                         # lossless only
  compack -skip-png -skip-ogg -skip-json -skip-text ./my-pack  # just ZIP everything
  compack -config my-config.yml ./my-pack                   # use settings from a config file
  compack -cache .compack-cache ./my-pack                   # reuse PNG/OGG results across runs

Flags:`

// config mirrors every user-facing setting. It is populated from (in order):
// defaults -> config.yml (if found / specified) -> explicit command-line flags.
// The yaml tags are the config.yml keys.
type config struct {
	Out        string `yaml:"out"`
	Threads    int    `yaml:"threads"`
	Quiet      bool   `yaml:"quiet"`
	Verbose    bool   `yaml:"verbose"`
	DryRun     bool   `yaml:"dry_run"`
	NoProgress bool   `yaml:"no_progress"`
	Events     bool   `yaml:"events"`
	Cache      string `yaml:"cache"`
	Zip        struct {
		// LocalNames repeats each name in its local file header, which
		// Minecraft never reads but 7-Zip needs to open the archive.
		LocalNames bool `yaml:"local_names"`
	} `yaml:"zip"`

	optim.Options `yaml:",inline"`

	// inPath is the input resource/data pack: either a directory or a .zip
	// archive.
	inPath      string
	configPath  string
	showVersion bool
}

// defaultConfig returns the built-in default settings used when neither a
// config file nor flags override them.
func defaultConfig() config {
	return config{
		Out:     "pack.zip",
		Threads: runtime.NumCPU(),
		Options: optim.Options{
			JSON: optim.JSONOptions{Minify: true},
			PNG: optim.PNGOptions{
				Recompress:   true,
				StripMeta:    true,
				Lossy:        true,
				QuantMin:     65,
				QuantMax:     90,
				ProtectAlpha: true,
			},
			OGG:  optim.OGGOptions{Optimize: true, StripComments: true, Quality: 4},
			Text: optim.TextOptions{Minify: true},
		},
	}
}

// offFlag is a boolean flag that turns every setting it points at off: the
// -skip-* aliases. A later flag on the command line still wins.
type offFlag []*bool

func (f offFlag) String() string   { return "false" }
func (f offFlag) IsBoolFlag() bool { return true }
func (f offFlag) Set(s string) error {
	on, err := strconv.ParseBool(s)
	if on {
		for _, p := range f {
			*p = false
		}
	}
	return err
}

// newFlagSet binds every flag to cfg, using cfg's current values as defaults.
func newFlagSet(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet("compack", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.configPath, "config", cfg.configPath, "path to a config.yml file with default settings (auto-loaded when present as ./config.yml)")
	fs.BoolVar(&cfg.showVersion, "version", false, "print the version and exit")
	fs.StringVar(&cfg.Out, "out", cfg.Out, "output ZIP file path")
	fs.IntVar(&cfg.Threads, "threads", cfg.Threads, "number of parallel file workers (0 = one per CPU)")
	fs.BoolVar(&cfg.Quiet, "q", cfg.Quiet, "suppress per-file log lines")
	fs.BoolVar(&cfg.Verbose, "v", cfg.Verbose, "verbose log (show file notes + skipped files)")
	fs.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "walk and optimize but do not write the ZIP")
	fs.BoolVar(&cfg.NoProgress, "no-progress", cfg.NoProgress, "never show the progress bar (default: auto when stderr is a TTY)")
	fs.BoolVar(&cfg.Events, "events", cfg.Events, "print progress, notes and the summary as JSON lines on stdout (for tools and the desktop app)")
	fs.StringVar(&cfg.Cache, "cache", cfg.Cache, "directory where PNG/OGG results are kept between runs, so unchanged files are not optimized again (off when empty)")
	fs.BoolVar(&cfg.Zip.LocalNames, "zip-local-names", cfg.Zip.LocalNames, "also write file names in local headers (bigger; Minecraft does not need them, 7-Zip does)")

	o := &cfg.Options
	fs.BoolVar(&o.JSON.Minify, "json-minify", o.JSON.Minify, "minify JSON/JSONC/.mcmeta files")
	fs.Var(offFlag{&o.JSON.Minify}, "skip-json", "do not minify JSON")

	fs.BoolVar(&o.PNG.Recompress, "png-recompress", o.PNG.Recompress, "recompress PNG IDAT with oxipng")
	fs.BoolVar(&o.PNG.StripMeta, "png-strip-meta", o.PNG.StripMeta, "drop ancillary PNG chunks (metadata removal)")
	fs.IntVar(&o.PNG.Level, "png-level", o.PNG.Level, "oxipng optimization preset (1-6, 0 = best-effort default 4, >6 = max)")
	fs.BoolVar(&o.PNG.KeepColorProfile, "png-keep-color-profile", o.PNG.KeepColorProfile, "keep gAMA/iCCP/cHRM/sRGB chunks")
	fs.BoolVar(&o.PNG.Lossy, "png-lossy", o.PNG.Lossy, "lossily remap RGB/RGBA PNGs to an 8-bit palette via embedded pngquant (libimagequant) before the lossless oxipng pass; matches PackSquash's compression level")
	fs.IntVar(&o.PNG.QuantMin, "png-quant-min", o.PNG.QuantMin, "pngquant --quality lower bound (0-100); below this quality the original pixels are kept")
	fs.IntVar(&o.PNG.QuantMax, "png-quant-max", o.PNG.QuantMax, "pngquant --quality upper bound (0-100); trades palette size vs quality")
	fs.BoolVar(&o.PNG.ProtectAlpha, "png-protect-alpha", o.PNG.ProtectAlpha, "skip the lossy pngquant pass for any RGBA PNG that actually uses its alpha channel (any non-fully-opaque pixel), preserving alpha values bit-for-bit; fully-opaque RGBA and RGB PNGs still get quantised")
	fs.Var(offFlag{&o.PNG.Lossy}, "skip-png-quant", "do not run the lossy pngquant palette step (alias for -png-lossy=false)")
	fs.Var(offFlag{&o.PNG.Recompress, &o.PNG.StripMeta, &o.PNG.Lossy}, "skip-png", "leave PNGs untouched (alias for -png-recompress=false -png-strip-meta=false -png-lossy=false)")

	fs.BoolVar(&o.OGG.Optimize, "ogg", o.OGG.Optimize, "losslessly shrink OGG/Ogg Vorbis via OptiVorbis (use -skip-ogg to disable)")
	fs.Var(offFlag{&o.OGG.Optimize}, "skip-ogg", "do not optimize OGGs (alias for -ogg=false)")
	fs.BoolVar(&o.OGG.StripComments, "ogg-strip-comments", o.OGG.StripComments, "remove OGG comment fields (artist/title/etc.); set -ogg-strip-comments=false to keep")
	fs.BoolVar(&o.OGG.Lossy, "ogg-lossy", o.OGG.Lossy, "re-encode OGGs at -ogg-quality (lossy; channels kept, sample rates above 48 kHz halved), keeping a re-encode only when it is at least 15% smaller")
	fs.IntVar(&o.OGG.Quality, "ogg-quality", o.OGG.Quality, "Vorbis quality of -ogg-lossy re-encodes (0-10)")

	fs.BoolVar(&o.Text.Minify, "text-minify", o.Text.Minify, "strip comments + blank lines from shaders/lang/properties")
	fs.Var(offFlag{&o.Text.Minify}, "skip-text", "do not minify shaders/lang/properties")
	return fs
}

// parseFlags parses flags and positional arguments in any order (the stdlib
// parser stops at the first positional), so `compack ./pack -out pack.zip`
// does not silently drop -out.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// parseArgs builds the config: defaults, then config.yml, then the flags.
// The flags are parsed twice: first to find -config, because the file's
// values must be in place before the explicit flags that override them.
func parseArgs(args []string) (config, error) {
	probe := defaultConfig()
	pfs := newFlagSet(&probe)
	pfs.SetOutput(io.Discard)
	pfs.Usage = func() {}
	_, _ = parseFlags(pfs, args) // errors are reported by the second pass

	cfg := defaultConfig()
	// Optional config file precedence:
	//   1. -config <path>  -> load exactly that path (error if it cannot be read)
	//   2. ./config.yml    -> auto-loaded if present in the working directory
	//   3. built-in defaults -> otherwise
	path := probe.configPath
	if path == "" {
		if _, err := os.Stat("config.yml"); err == nil {
			path = "config.yml"
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			err = yaml.Unmarshal(data, &cfg)
		}
		if err != nil {
			return cfg, fmt.Errorf("load config %q: %w", path, err)
		}
		if cfg.Out == "" {
			cfg.Out = defaultConfig().Out
		}
	}

	fs := newFlagSet(&cfg)
	positional, err := parseFlags(fs, args)
	if err != nil {
		return cfg, err
	}
	if cfg.showVersion {
		return cfg, nil
	}
	switch len(positional) {
	case 0:
		fs.Usage()
		return cfg, errors.New("missing input path (directory or .zip)")
	case 1:
		cfg.inPath = positional[0]
	default:
		return cfg, fmt.Errorf("unexpected argument %q (one input path only)", positional[1])
	}
	if cfg.Threads < 1 {
		cfg.Threads = runtime.NumCPU()
	}
	return cfg, nil
}

func main() {
	log.SetFlags(0)
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "compack:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}
	if cfg.showVersion {
		fmt.Println("compack", version)
		return nil
	}
	handleInterrupts()
	onInterrupt(optim.Cleanup)
	defer optim.Cleanup()
	removeStale(os.TempDir())
	optim.TempPrefix = tempPrefix()

	rep := newReporter(cfg, os.Stdout)
	if err := build(cfg, rep); err != nil {
		rep.fail(err)
		return err
	}
	return nil
}

// interruptCleanups run when the process is interrupted (SIGINT/SIGTERM, e.g.
// the desktop app's Stop button), so no temp file outlives a cancelled run.
var interruptCleanups struct {
	sync.Mutex
	fns []func()
}

func onInterrupt(fn func()) {
	interruptCleanups.Lock()
	defer interruptCleanups.Unlock()
	interruptCleanups.fns = append(interruptCleanups.fns, fn)
}

func handleInterrupts() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		interruptCleanups.Lock()
		for _, fn := range interruptCleanups.fns {
			fn()
		}
		os.Exit(130)
	}()
}

// fileSpec is one discovered resource pack file.
type fileSpec struct {
	rel  string // forward-slash relative path within the input directory / archive
	size int64
	// read returns the raw file bytes. For a directory input this reads from
	// the filesystem; for a .zip input it reads from the archive entry.
	read func() ([]byte, error)
}

// entry is one optimized file, ready for the ZIP writer.
type entry struct {
	rel     string
	orig    int64 // input size
	size    int64 // optimized (uncompressed) size
	changed bool
	note    string
	method  uint16 // zip.Store or zip.Deflate
	crc     uint32 // CRC-32 of the optimized bytes
	payload []byte // the bytes as stored in the ZIP (deflated or not)
}

// summary is the outcome of a run.
type summary struct {
	Files       int    `json:"files"`
	Optimized   int    `json:"optimized"`
	Notes       int    `json:"notes"`
	InputBytes  int64  `json:"input_bytes"`  // the input .zip, or the sum of the folder's files
	OutputBytes int64  `json:"output_bytes"` // the written ZIP (what it would be on a dry run)
	DedupBytes  int64  `json:"dedup_bytes"`
	Millis      int64  `json:"ms"`
	Out         string `json:"out,omitempty"` // empty on a dry run
	SHA1        string `json:"sha1,omitempty"`
}

func build(cfg config, rep reporter) error {
	start := time.Now()
	outAbs, err := filepath.Abs(cfg.Out)
	if err != nil {
		return err
	}
	files, inputBytes, closeInput, err := openInput(cfg.inPath, outAbs)
	if err != nil {
		return err
	}
	defer closeInput()
	if len(files) == 0 {
		return fmt.Errorf("no files found under %q", cfg.inPath)
	}

	// Create the output before the long part, so an unwritable folder fails
	// at once. It is a temp file in the destination's directory, renamed into
	// place at the end (atomic, never crosses filesystems).
	var out *os.File
	if !cfg.DryRun {
		removeStale(filepath.Dir(outAbs))
		out, err = os.CreateTemp(filepath.Dir(outAbs), tempPrefix()+"*.zip.tmp")
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		tmp := out.Name()
		onInterrupt(func() { _ = os.Remove(tmp) })
		defer func() {
			if out != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
			}
		}()
	}

	rep.start(len(files))
	entries, err := optimizeAll(cfg, files, rep)
	if err != nil {
		return err
	}
	optim.Cleanup()

	s := summary{Files: len(entries), InputBytes: inputBytes}
	for _, e := range entries {
		if e.changed {
			s.Optimized++
		}
		rep.file(e)
	}
	for _, e := range entries {
		if e.note != "" {
			s.Notes++
			rep.note(e.rel, e.note)
		}
	}

	if cfg.DryRun {
		s.OutputBytes, s.DedupBytes, err = writeZip(io.Discard, entries, cfg.Zip.LocalNames)
		if err != nil {
			return err
		}
	} else {
		h := sha1.New()
		s.OutputBytes, s.DedupBytes, err = writeZip(io.MultiWriter(out, h), entries, cfg.Zip.LocalNames)
		if err == nil {
			err = out.Chmod(0o644) // CreateTemp makes it private; a pack is meant to be served
		}
		if err == nil {
			err = out.Close()
		}
		if err == nil {
			err = os.Rename(out.Name(), outAbs)
		}
		if err != nil {
			return fmt.Errorf("write zip: %w", err)
		}
		out = nil
		s.Out = outAbs
		s.SHA1 = hex.EncodeToString(h.Sum(nil))
	}
	s.Millis = time.Since(start).Milliseconds()
	rep.done(s)
	return nil
}

// optimizeAll optimizes and compresses every file on cfg.Threads workers and
// returns the entries in path order.
func optimizeAll(cfg config, files []fileSpec, rep reporter) ([]entry, error) {
	var cache *resultCache
	if cfg.Cache != "" {
		var err error
		if cache, err = openCache(cfg.Cache, cfg.Options); err != nil {
			return nil, fmt.Errorf("open cache: %w", err)
		}
	}

	// Largest files first, so a big texture or song never starts last and
	// runs alone while the other workers sit idle.
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return files[order[a]].size > files[order[b]].size })

	entries := make([]entry, len(files))
	errs := make([]error, len(files))
	jobs := make(chan int)
	done := make(chan int, len(files))
	optim.Extract()
	for w := 0; w < cfg.Threads; w++ {
		go func() {
			c := newCompressor()
			for i := range jobs {
				entries[i], errs[i] = process(files[i], cfg.Options, cache, c)
				done <- i
			}
		}()
	}
	go func() {
		for _, i := range order {
			jobs <- i
		}
		close(jobs)
	}()
	for n := 1; n <= len(files); n++ {
		i := <-done
		rep.progress(n, len(files), files[i].rel)
	}
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("optimizing %s: %w", files[i].rel, err)
		}
	}
	return entries, nil
}

// process reads, optimizes and compresses one file.
func process(f fileSpec, opts optim.Options, cache *resultCache, c *compressor) (entry, error) {
	data, err := f.read()
	if err != nil {
		return entry{}, err
	}
	spec := optim.Lookup(f.rel)
	var r optim.Result
	if cache != nil && spec.Slow {
		r, err = cache.optimize(spec, f.rel, data, opts)
	} else {
		r, err = spec.Optimize(f.rel, data, opts)
	}
	if err != nil {
		return entry{}, err
	}
	e := entry{
		rel:     f.rel,
		orig:    int64(len(data)),
		size:    int64(len(r.Data)),
		changed: r.Changed,
		note:    r.Note,
		crc:     crc32.ChecksumIEEE(r.Data),
	}
	e.method, e.payload = c.pack(r.Data, r.Method)
	return e, nil
}

// compressor deflates one worker's entries, reusing its flate state: setting
// one up costs more than compressing a typical pack JSON.
type compressor struct {
	buf bytes.Buffer
	w   *flate.Writer
}

func newCompressor() *compressor {
	c := &compressor{}
	c.w, _ = flate.NewWriter(&c.buf, flate.BestCompression)
	return c
}

// pack returns how data is stored in the ZIP: deflated when the optimizer
// asked for it and that is actually smaller, as it is otherwise. The writer
// is deterministic, so identical files get identical payloads (deduplicated
// by the ZIP writer).
func (c *compressor) pack(data []byte, method uint16) (uint16, []byte) {
	if method != zip.Deflate || len(data) == 0 {
		return zip.Store, data
	}
	c.buf.Reset()
	c.w.Reset(&c.buf)
	_, _ = c.w.Write(data)
	_ = c.w.Close()
	if c.buf.Len() >= len(data) {
		return zip.Store, data
	}
	return zip.Deflate, bytes.Clone(c.buf.Bytes())
}

// openInput lists the input's files (a directory or a .zip archive), its size
// on disk and a function releasing it. outAbs is left out of a directory
// input, so a previous output inside the pack is not packed again.
func openInput(path, outAbs string) (files []fileSpec, size int64, closeFn func(), err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("stat input: %w", err)
	}
	if info.IsDir() {
		files, err = discover(path, outAbs)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("walk input dir: %w", err)
		}
		for _, f := range files {
			size += f.size
		}
		return files, size, func() {}, nil
	}
	if !strings.EqualFold(filepath.Ext(path), ".zip") {
		return nil, 0, nil, fmt.Errorf("input path %q is neither a directory nor a .zip file", path)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("open input zip %q: %w", path, err)
	}
	return discoverZipEntries(zr.File), info.Size(), func() { _ = zr.Close() }, nil
}

// discover returns the sorted list of regular files under root, with rel
// paths using forward slashes. Hidden directories (starting with '.') are
// skipped so transient editor state (.git, .vscode, .idea) is not bundled.
func discover(root, skip string) ([]fileSpec, error) {
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
		if !d.Type().IsRegular() || path == skip {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		abs := path
		out = append(out, fileSpec{rel: rel, size: info.Size(), read: func() ([]byte, error) {
			return os.ReadFile(abs)
		}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// discoverZipEntries turns a ZIP archive's entries into fileSpecs whose bytes
// are read lazily through the archive (archive/zip reads entries concurrently
// through ReadAt). Directory entries and non-regular files are skipped;
// leading "./" prefixes are stripped so rel paths match what discover would
// produce for a folder.
func discoverZipEntries(entries []*zip.File) []fileSpec {
	out := make([]fileSpec, 0, len(entries))
	for _, f := range entries {
		fi := f.FileInfo()
		if fi.IsDir() || !fi.Mode().IsRegular() {
			continue
		}
		entry := f
		out = append(out, fileSpec{
			rel:  strings.TrimPrefix(filepath.ToSlash(f.Name), "./"),
			size: int64(f.UncompressedSize64),
			read: func() ([]byte, error) {
				rc, err := entry.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
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
