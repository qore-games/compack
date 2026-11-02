package optim

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// pngCRC is exported at the package level because png_test.go reuses it to
// synthesise PNG chunks without pulling in another crc32 table.
var pngCRC = crc32.MakeTable(crc32.IEEE)

// optimizePNG runs the embedded pngquant (libimagequant) and oxipng binaries
// against the input PNG. The pipeline is:
//
//  1. (optional) pngquant: lossily remap RGB/RGBA (color-type 2/6) PNGs to an
//     8-bit palette when the result still meets the configured quality range.
//     This step performs what lossless-only tools (oxipng) cannot, and
//     it is what collapses photographic assets such as panoramas
//     to ~25% of their lossless size.
//  2. oxipng: 100% lossless recompression of the (possibly paletted) IDAT,
//     plus ancillary chunk stripping.
//
// APNGs (detected via an acTL chunk) are passed through unchanged, because
// oxipng would also optimise the per-frame payloads and Minecraft's APNG
// playback expects them untouched.
//
// When no embedded binary exists for the current GOOS/GOARCH the missing step
// is silently skipped (the build still succeeds). On windows/arm64 none of
// the binaries are embedded, so PNGs pass through untouched entirely.
func optimizePNG(path string, data []byte, opts Options) (Result, error) {
	out, note := tryOptimizePNG(data, opts.PNG)
	if out == nil {
		return Result{Data: data, Method: zip.Deflate, Note: note}, nil
	}
	return Result{Data: out, Method: zip.Store, Changed: true, Note: note}, nil
}

// tryOptimizePNG returns the smallest of the original, the quantized and the
// oxipng outputs, or nil when nothing beat the original: the input was not a
// valid PNG, the file is an APNG, every step is disabled or failed, or no
// embedded binary exists for this platform. note explains a tool failure.
func tryOptimizePNG(data []byte, opts PNGOptions) (out []byte, note string) {
	if !opts.Recompress && !opts.StripMeta && !opts.Lossy {
		return nil, ""
	}
	if !bytes.HasPrefix(data, pngSignature) || isAPNG(data) {
		return nil, ""
	}

	// Stage 1 (optional, lossy): pngquant → 8-bit palette. A failure (not
	// just "binary missing on this platform") is noted but never aborts the
	// build; the unquantized input carries on to oxipng.
	best := data
	if opts.Lossy {
		q, err := quantizePNG(data, opts)
		if err != nil && !errors.Is(err, ErrBinNotFound) {
			note = "pngquant failed: " + err.Error()
		}
		if q != nil && len(q) < len(best) {
			best = q
		}
	}

	// Stage 2 (lossless): oxipng strips ancillary chunks and recompresses
	// the IDAT of whichever input is smallest so far.
	if opts.Recompress || opts.StripMeta {
		o, err := runOxipng(best, opts)
		if err != nil && !errors.Is(err, ErrBinNotFound) {
			note = "oxipng failed: " + err.Error()
		}
		if len(o) > 0 && len(o) < len(best) {
			best = o
		}
	}
	if len(best) >= len(data) {
		return nil, note
	}
	return best, note
}

// runOxipng losslessly recompresses a PNG with the embedded oxipng.
func runOxipng(data []byte, opts PNGOptions) ([]byte, error) {
	bin, err := extractBin("oxipng")
	if err != nil {
		return nil, err
	}
	// One thread per oxipng: compack already runs one file per CPU.
	args := []string{"-o", oxipngLevel(opts.Level), "-t", "1"}
	if opts.StripMeta {
		if opts.KeepColorProfile {
			// -s = --strip safe: keep cICP/iCCP/sRGB/pHYs/acTL/fcTL/fdAT.
			args = append(args, "-s")
		} else {
			// Strip every non-critical chunk (keeps IHDR/PLTE/tRNS/IEND).
			args = append(args, "--strip", "all")
		}
	}
	args = append(args, "--stdout", "-")
	return runTool(bin, args, data)
}

// quantizePNG runs the embedded pngquant binary (built on libimagequant) to
// lossily remap an RGB/RGBA PNG to an 8-bit palette. It returns nil (keep the
// original) when the source is already palette/grayscale, uses its alpha
// channel while ProtectAlpha is set, or pngquant could not reach the minimum
// quality (exit 99) or a smaller file.
func quantizePNG(data []byte, opts PNGOptions) ([]byte, error) {
	colorType, err := pngColorType(data)
	if err != nil {
		return nil, nil
	}
	// 0 = gray, 3 = palette, 4 = gray+alpha. Skip — pngquant wouldn't
	// improve them in a way worth the process spawn cost.
	if colorType == 0 || colorType == 3 || colorType == 4 {
		return nil, nil
	}

	// Alpha-channel guard. Only color-type 6 (RGBA) carries an alpha channel
	// that pngquant could mangle. Fully-opaque RGBA images (every alpha ==
	// 255) are NOT matched here, so they keep benefiting from the lossy pass.
	if colorType == 6 && opts.ProtectAlpha && usesAlphaChannel(data) {
		return nil, nil
	}

	bin, err := extractBin("pngquant")
	if err != nil {
		return nil, err
	}
	args := []string{"--skip-if-larger", "--strip"}
	if q := pngQuantRange(opts.QuantMin, opts.QuantMax); q != "" {
		args = append(args, "--quality="+q)
	}
	out, err := runTool(bin, append(args, "-"), data)
	// Exit code 99 = quality below the requested minimum, 98 = not smaller
	// (--skip-if-larger): pngquant's contract is "image won't be saved".
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 98 || ee.ExitCode() == 99) {
		return nil, nil
	}
	if err != nil || len(out) == 0 || len(out) >= len(data) {
		return nil, err
	}
	return out, nil
}

// runTool pipes data through an external tool and returns its stdout. A
// non-zero exit is an error carrying the first line of stderr.
func runTool(bin string, args []string, data []byte) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runCmd(cmd); err != nil {
		if line := firstLine(stderr.String()); line != "" {
			return nil, &toolError{err: err, msg: line}
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// toolError keeps the exit status (errors.As finds the *exec.ExitError) while
// reading as the tool's own message.
type toolError struct {
	err error
	msg string
}

func (e *toolError) Error() string { return e.msg }
func (e *toolError) Unwrap() error { return e.err }

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// oxipngLevel maps the user-facing PNGLevel option to a valid oxipng -o
// argument. 0 means "best-effort default" (preset 4), 1..6 use oxipng presets
// verbatim, anything >6 forces the slow "max" preset.
func oxipngLevel(level int) string {
	switch {
	case level <= 0:
		return strconv.Itoa(4)
	case level > 6:
		return "max"
	default:
		return strconv.Itoa(level)
	}
}

// pngQuantRange clamps the user-provided min/max quality values into the
// 0-100 range pngquant expects and returns "min-max" (or empty when both
// are 0, in which case pngquant uses its internal defaults).
func pngQuantRange(min, max int) string {
	if min < 0 {
		min = 0
	}
	if min > 100 {
		min = 100
	}
	if max < 0 {
		max = 0
	}
	if max > 100 {
		max = 100
	}
	if max < min {
		max = min
	}
	if min == 0 && max == 0 {
		return ""
	}
	return strconv.Itoa(min) + "-" + strconv.Itoa(max)
}

// pngColorType returns the PNG IHDR color-type byte (0/2/3/4/6). It parses
// only the first chunk after the signature; on malformed input it returns an
// error so the caller can fall back to a safe path (pngquant would also
// reject the file).
func pngColorType(data []byte) (byte, error) {
	const ihdrLen = 4 + 4 + 13 + 4 // length + type + body + crc
	if len(data) < len(pngSignature)+ihdrLen {
		return 0, errors.New("png too short for IHDR")
	}
	if !bytes.HasPrefix(data, pngSignature) {
		return 0, errors.New("not a png")
	}
	// bytes 12..16 hold the IHDR chunk type. Color type is at offset 9 of
	// the IHDR body = absolute offset 8 (sig) + 4 (length) + 4 (type) + 9.
	ct := data[len(pngSignature)+4+4+9]
	return ct, nil
}

// isAPNG reports whether the PNG carries an acTL animation-control chunk. We
// scan the chunk stream manually instead of using image/png because the stdlib
// decoder rejects some chunk sequences that pack authors (and Minecraft) accept.
func isAPNG(data []byte) bool {
	r := bytes.NewReader(data[len(pngSignature):])
	for {
		length, err := readU32(r)
		if err == io.EOF {
			break
		} else if err != nil {
			return false
		}
		typ := make([]byte, 4)
		if _, err := io.ReadFull(r, typ); err != nil {
			return false
		}
		if string(typ) == "acTL" {
			return true
		}
		if _, err := io.CopyN(io.Discard, r, int64(length)+4); err != nil {
			return false
		}
	}
	return false
}

// readU32 reads a big-endian uint32 from r. EOF is propagated so isAPNG can
// tell stream end apart from a malformed length.
func readU32(r io.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b[:]), nil
}

// usesAlphaChannel reports whether the source PNG carries any pixel with a
// meaningful alpha value (one that is neither fully transparent nor fully
// opaque). Such a texture encodes semantic data in its alpha channel (e.g.
// Minecraft trim/leather tint keys, glass, ice, leaves, particle fades) that
// the libimagequant palette pass would round towards the nearest palette
// entry — destroying thin markers entirely and shifting mid-range alphas.
// When this returns true the caller skips pngquant so the image falls through
// to the lossless oxipng stage and every alpha value is preserved bit-for-bit.
//
// Fully-transparent (alpha == 0) and fully-opaque (alpha == 255) pixels are
// NOT considered "alpha data": alpha=0 carries no semantic information (it's
// just a hole) and alpha=255 means fully visible, both being palette
// endpoints that pngquant preserves losslessly. So a typical hard-edged
// cutout (e.g. an icon with a transparent background and an opaque subject)
// still gets to benefit from the lossy palette pass. On any decode error
// it returns false so pngquant still gets a chance to decide what to do with
// the file.
func usesAlphaChannel(data []byte) bool {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	// Color-type 6 decodes to NRGBA (8-bit) or NRGBA64 (16-bit): scan the
	// alpha bytes directly instead of boxing every pixel through At().
	switch img := img.(type) {
	case *image.NRGBA:
		for i := 3; i < len(img.Pix); i += 4 {
			if a := img.Pix[i]; a != 0 && a != 0xff {
				return true
			}
		}
		return false
	case *image.NRGBA64:
		for i := 6; i < len(img.Pix); i += 8 {
			a := uint16(img.Pix[i])<<8 | uint16(img.Pix[i+1])
			if a != 0 && a != 0xffff {
				return true
			}
		}
		return false
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0 && a != 0xffff {
				return true
			}
		}
	}
	return false
}

// toolMessage is the first line of a failed tool's stderr, or its exit error.
func toolMessage(stderr string, err error) string {
	if line := firstLine(stderr); line != "" {
		return line
	}
	return err.Error()
}
