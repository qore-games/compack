// Package optim applies per-file optimization strategies to Minecraft
// resource / data pack files. Each optimizer takes the file's raw bytes and
// returns the optimized bytes together with a hint of how the result should be
// stored inside a ZIP archive.
package optim

import (
	"archive/zip"
	"sort"
	"strings"
	"sync"
)

// Options controls the behaviour of every optimizer.
type Options struct {
	// JSON strip whitespace, comments and unnecessary separators in JSON-like
	// files (.json, .jsonc, .mcmeta, .mcmetac).
	JSONMinify bool

	// PNG lossless optimizer: strip ancillary chunks and recompress IDAT using
	// the strongest zlib level available.
	PNGRecompress   bool
	PNGStripMeta    bool
	PNGLevel        int  // oxipng preset, 1-6 (0 = default preset 4; >6 = max).
	PNGKeepColorMng bool // keep gAMA/iCCP/cHRM/sRGB chunks if present.

	// PNG lossy step (runs *before* oxipng): convert RGB/RGBA images to an
	// 8-bit palette using the embedded pngquant CLI (built on libimagequant).
	// When PNGLossyQuant is true, color-type 2/6 PNGs whose palette would still
	// meet PNGQuantMin..PNGQuantMax quality (0-100, JPEG-like) are reduced to
	// color-type 3 (indexed). Already-paletted, grayscale and APNG images are
	// left alone. Pixel values are NOT preserved bit-for-bit.
	PNGLossyQuant bool
	PNGQuantMin   int // 0-100, lower bound for --quality (pngquant exit 99 = pass-through).
	PNGQuantMax   int // 0-100, upper bound for --quality.

	// OGG is optimized via the embedded OptiVorbis binary. OptiVorbis is a
	// fully lossless optimizer: it shrinks the Ogg container and strips
	// metadata without touching the decoded audio samples. OGGOptimize gates
	// the whole step. OGGStripComments removes the Vorbis comment header's
	// user fields (artist/title/etc.); set to false to keep them verbatim.
	OGGOptimize      bool
	OGGStripComments bool

	// Text optimizers (.lang, .properties, .vsh/.fsh/.glsl): strip comments and
	// blank lines, collapse redundant whitespace.
	TextMinify bool
}

// Result describes the bytes returned by an optimizer.
type Result struct {
	Data    []byte
	Method  uint16 // zip.Store or zip.Deflate
	Changed bool   // true if the optimizer modified the bytes.
	Skipped bool   // true if the optimizer chose to keep input as-is (e.g. tiny JSON).
	Note    string // optional human-readable note (e.g. a warning).
}

// Optimizer is a per-path transformation function. The path is the file's
// relative path (forward slashes) so optimizers can react to extension or
// special-case files such as pack.png.
type Optimizer func(path string, data []byte, opts Options) (Result, error)

var (
	mu       sync.RWMutex
	registry = make(map[string]Optimizer)
)

// Register binds an optimizer to one or more file extensions (with or without
// the leading dot, lower-cased). Registers in a sorted order so behavior is
// deterministic; later calls win.
func Register(opt Optimizer, exts ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, e := range exts {
		ext := strings.ToLower(strings.TrimPrefix(e, "."))
		registry["."+ext] = opt
	}
}

// Lookup returns the optimizer registered for the given path (by extension).
// Returns nil when no optimizer handles this path.
func Lookup(path string) Optimizer {
	mu.RLock()
	defer mu.RUnlock()
	return registry[strings.ToLower(extension(path))]
}

// Optimize applies the right optimizer for the given path or returns the
// input bytes unchanged (Deflate-stored by the zip writer) if no optimizer
// handles it.
func Optimize(path string, data []byte, opts Options) (Result, error) {
	if opt := Lookup(path); opt != nil {
		return opt(path, data, opts)
	}
	return Result{Data: data, Method: zip.Deflate, Changed: false}, nil
}

func extension(path string) string {
	idx := strings.LastIndexByte(path, '.')
	if idx == -1 {
		return ""
	}
	return path[idx:]
}

// Extensions returns the sorted list of registered extensions.
func Extensions() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
