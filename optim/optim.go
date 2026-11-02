// Package optim applies per-file optimization strategies to Minecraft
// resource / data pack files. Each optimizer takes the file's raw bytes and
// returns the optimized bytes together with a hint of how the result should be
// stored inside a ZIP archive.
package optim

import (
	"archive/zip"
	"strings"
)

// Options controls the behaviour of every optimizer. The yaml tags are the
// config.yml sections, so a config file decodes straight onto the defaults.
type Options struct {
	JSON JSONOptions `yaml:"json"`
	PNG  PNGOptions  `yaml:"png"`
	OGG  OGGOptions  `yaml:"ogg"`
	Text TextOptions `yaml:"text"`
}

// JSONOptions: strip whitespace and comments in JSON-like files (.json,
// .jsonc, .mcmeta, .mcmetac).
type JSONOptions struct {
	Minify bool `yaml:"minify"`
}

type PNGOptions struct {
	// Lossless oxipng pass: strip ancillary chunks and recompress IDAT.
	Recompress       bool `yaml:"recompress"`
	StripMeta        bool `yaml:"strip_meta"`
	Level            int  `yaml:"level"`              // oxipng preset, 1-6 (0 = default preset 4; >6 = max).
	KeepColorProfile bool `yaml:"keep_color_profile"` // keep gAMA/iCCP/cHRM/sRGB chunks if present.

	// Lossy step (runs *before* oxipng): convert RGB/RGBA images to an 8-bit
	// palette using the embedded pngquant CLI (built on libimagequant).
	// Color-type 2/6 PNGs whose palette would still meet QuantMin..QuantMax
	// quality (0-100, JPEG-like) are reduced to color-type 3 (indexed).
	// Already-paletted, grayscale and APNG images are left alone. Pixel
	// values are NOT preserved bit-for-bit.
	Lossy    bool `yaml:"lossy_quant"`
	QuantMin int  `yaml:"quant_min"` // 0-100, lower bound for --quality (pngquant exit 99 = pass-through).
	QuantMax int  `yaml:"quant_max"` // 0-100, upper bound for --quality.

	// ProtectAlpha pre-scans RGBA (color-type 6) PNGs for pixels with a
	// meaningful alpha value (0 < alpha < 255). When set, textures that
	// actually encode semantic data in their alpha channel (e.g. Minecraft
	// trim/leather tint keys, glass, ice, leaves) skip the lossy pngquant
	// pass and fall through to the lossless oxipng stage, preserving every
	// alpha value bit-for-bit — the palette quantiser would otherwise round
	// alpha towards the nearest palette entry, collapsing thin markers to
	// fully transparent and shifting mid-range alphas.
	//
	// Hard-edged cutouts (alpha == 0 or 255 only — e.g. an icon with a
	// transparent background and an opaque subject) are NOT matched: those
	// endpoints are palette endpoints pngquant preserves losslessly, and
	// alpha=0 carries no semantic data. Plain RGB images (color-type 2)
	// carry no alpha channel at all and always go through the lossy pass.
	ProtectAlpha bool `yaml:"protect_alpha"`
}

// OGGOptions: OGG is optimized via the embedded OptiVorbis binary. OptiVorbis
// is a fully lossless optimizer: it shrinks the Ogg container and strips
// metadata without touching the decoded audio samples. Optimize gates the
// whole step. StripComments removes the Vorbis comment header's user fields
// (artist/title/etc.); set to false to keep them verbatim.
//
// Lossy (opt-in) first re-encodes every file at Quality (Vorbis 0-10) with
// the embedded oggshrink, keeping the channels and halving sample rates above
// 48 kHz; a re-encode is only kept when it is at least 15% smaller.
type OGGOptions struct {
	Optimize      bool `yaml:"optimize"`
	StripComments bool `yaml:"strip_comments"`
	Lossy         bool `yaml:"lossy"`
	Quality       int  `yaml:"quality"`
}

// TextOptions: text optimizers (.lang, .properties, .vsh/.fsh/.glsl) strip
// comments and blank lines and collapse redundant whitespace.
type TextOptions struct {
	Minify bool `yaml:"minify"`
}

// Result describes the bytes returned by an optimizer.
type Result struct {
	Data    []byte
	Method  uint16 // zip.Store (already compressed, don't deflate) or zip.Deflate
	Changed bool   // true if the optimizer modified the bytes.
	Note    string // optional human-readable note (e.g. a warning).
}

// Optimizer is a per-path transformation function. The path is the file's
// relative path (forward slashes) so optimizers can react to extension or
// special-case files such as pack.png.
type Optimizer func(path string, data []byte, opts Options) (Result, error)

// Spec is a registered optimizer.
type Spec struct {
	Kind     string // short tag shown in progress output ("PNG", "JSON"…)
	Slow     bool   // runs an external tool: worth caching between runs
	Optimize Optimizer
}

// Pass is the Spec of files no optimizer handles: they are stored as they are.
var Pass = Spec{Kind: "PASS", Optimize: func(_ string, data []byte, _ Options) (Result, error) {
	return Result{Data: data, Method: zip.Deflate}, nil
}}

// registry is filled by init (register.go) and only read afterwards.
var registry = make(map[string]Spec)

// Register binds an optimizer to one or more file extensions (with or without
// the leading dot, case-insensitive); later calls win. Call it from init only.
func Register(spec Spec, exts ...string) {
	for _, e := range exts {
		registry["."+strings.ToLower(strings.TrimPrefix(e, "."))] = spec
	}
}

// Lookup returns the optimizer registered for the given path (by extension),
// or Pass.
func Lookup(path string) Spec {
	if spec, ok := registry[strings.ToLower(extension(path))]; ok {
		return spec
	}
	return Pass
}

func extension(path string) string {
	idx := strings.LastIndexByte(path, '.')
	if idx == -1 || strings.IndexByte(path[idx:], '/') >= 0 {
		return ""
	}
	return path[idx:]
}
