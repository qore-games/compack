package optim

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os/exec"
	"strconv"
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
//     This step performs that lossless-only tools (oxipng) cannot, and
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
	if !opts.PNGRecompress && !opts.PNGStripMeta && !opts.PNGLossyQuant {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	out, ok, err := tryOptimizePNG(data, opts)
	if err != nil {
		return Result{Data: data, Method: zip.Deflate, Note: "png optimize failed: " + err.Error()}, nil
	}
	if !ok {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	return Result{Data: out, Method: zip.Store, Changed: len(out) != len(data)}, nil
}

// tryOptimizePNG is the inner entry-point kept as a separate function so the
// png_test.go unit tests can exercise it directly without going through the
// optimizer registry. ok=false means "pass through unchanged": either no
// embedded binary exists for this platform, the input was not a valid PNG, the
// file is an APNG, oxipng failed, or oxipng produced output that was not
// smaller than the original.
func tryOptimizePNG(data []byte, opts Options) ([]byte, bool, error) {
	if !opts.PNGRecompress && !opts.PNGStripMeta && !opts.PNGLossyQuant {
		return nil, false, nil
	}
	if len(data) < len(pngSignature) || !bytes.HasPrefix(data, pngSignature) {
		return nil, false, nil
	}
	if isAPNG(data) {
		return nil, false, nil
	}

	// Stage 1 (optional, lossy): pngquant → 8-bit palette. We only attempt
	// quantization when the user opted in AND the source PNG is in a color
	// space that benefits from it (true-color RGB or RGBA, i.e. PNG color
	// type 2 or 6). Already-paletted (type 3) and grayscale (type 0/4)
	// images are skipped: pngquant would just re-emit them, wasting a
	// process spawn per file.
	quantized, qnote, qok := data, "", false
	if opts.PNGLossyQuant {
		if q, note, ok, err := quantizePNG(data, opts); err == nil {
			quantized, qnote, qok = q, note, ok
		} else if !errors.Is(err, ErrBinNotFound) {
			// A genuine pngquant failure (not just "binary missing on this
			// platform") is noted but never aborts the build; we fall back
			// to the unquantized input below.
			qnote = "pngquant failed: " + err.Error()
		}
	}

	// Stage 2 (lossless): oxipng strips ancillary chunks and recompresses
	// the IDAT of either the paletted output (when quantization helped) or
	// the original PNG. If both oxipng is disabled (the user asked for
	// lossy-only via -png-lossy -skip-png) we still want to honor the
	// quantization result.
	if !opts.PNGRecompress && !opts.PNGStripMeta {
		if qok && len(quantized) > 0 && len(quantized) < len(data) {
			return quantized, true, nil
		}
		if qnote != "" {
			return nil, false, nil
		}
		return nil, false, nil
	}

	bin, err := extractBin("oxipng")
	if err != nil {
		if errors.Is(err, ErrBinNotFound) {
			// No oxipng for this platform. If quantization did produce a
			// smaller paletted PNG, still return it so the user gets the
			// benefit of the lossy pass.
			if qok && len(quantized) > 0 && len(quantized) < len(data) {
				return quantized, true, nil
			}
			return nil, false, nil
		}
		return nil, false, err
	}

	args := []string{"-o", oxipngLevel(opts.PNGLevel)}
	if opts.PNGStripMeta {
		if opts.PNGKeepColorMng {
			// -s = --strip safe: keep cICP/iCCP/sRGB/pHYs/acTL/fcTL/fdAT.
			args = append(args, "-s")
		} else {
			// Strip every non-critical chunk (matches the previous behaviour
			// of dropping everything that isn't IHDR/PLTE/tRNS/IEND).
			args = append(args, "--strip", "all")
		}
	}
	args = append(args, "--stdout", "-")

	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(quantized)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		// oxipng exited non-zero: malformed or unsupported PNG, pass through
		// without surfacing an error so one bad file doesn't fail the build.
		// But if pngquant gave us a smaller paletted input, prefer that.
		if qok && len(quantized) > 0 && len(quantized) < len(data) {
			return quantized, true, nil
		}
		_ = errb
		return nil, false, nil
	}
	result := out.Bytes()
	if len(result) == 0 {
		if qok && len(quantized) > 0 && len(quantized) < len(data) {
			return quantized, true, nil
		}
		return nil, false, nil
	}
	// Compare against the *original* (pre-quantization) size so the
	// quantization step gets credit for the savings.
	if len(result) >= len(data) {
		// Final result isn't smaller than the original. If quantization
		// alone already beat the original, ship that — otherwise skip.
		if qok && len(quantized) > 0 && len(quantized) < len(data) {
			return quantized, true, nil
		}
		return nil, false, nil
	}
	return result, true, nil
}

// quantizePNG runs the embedded pngquant binary (built on libimagequant) to
// lossily remap an RGB/RGBA PNG to an 8-bit palette.
//
// ok=true means quantization succeeded and produced a smaller PNG: out should
// be used in place of the original. ok=false means the result was not smaller
// (or was below --quality min, or pngquant isn't embedded for this platform
// via ErrBinNotFound, or the source is already palette/grayscale): the caller
// should keep the original bytes.
//
// pngquant's --quality min-max: when the achievable quality would drop below
// min, pngquant exits 99 and writes the 24-bit original to stdout. We detect
// that case and return ok=false so the caller falls back to lossless oxipng
// of the original.
func quantizePNG(data []byte, opts Options) (out []byte, note string, ok bool, err error) {
	colorType, err := pngColorType(data)
	if err != nil {
		return data, "", false, nil
	}
	// 0 = gray, 3 = palette. Skip — pngquant wouldn't improve them in a
	// way worth the process spawn cost.
	if colorType == 0 || colorType == 3 || colorType == 4 {
		return data, "", false, nil
	}

	bin, err := extractBin("pngquant")
	if err != nil {
		return data, "", false, err
	}

	args := []string{
		"--quality=" + pngQuantRange(opts.PNGQuantMin, opts.PNGQuantMax),
		"--skip-if-larger",
		"--strip",
		"-",
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Exit code 99 = quality below the requested minimum; pngquant's
		// contract is "image won't be saved", and it writes the original
		// 24-bit pixels to stdout. We treat this as "skip quantization".
		if ee, _ := err.(*exec.ExitError); ee != nil && ee.ExitCode() == 99 {
			return data, "", false, nil
		}
		// Any other non-zero exit: pass through unchanged (with a note
		// surfaced via the empty stderr, which we ignore here).
		return data, "pngquant exit " + err.Error(), false, nil
	}
	result := stdout.Bytes()
	if len(result) == 0 || len(result) >= len(data) {
		return data, "", false, nil
	}
	return result, "", true, nil
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
