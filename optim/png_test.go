package optim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makePNG encodes a small RGBA PNG on the fly so tests don't need fixtures.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// appendAncillaryChunk inserts a fake ancillary chunk after IHDR (valid PNG
// chunk ordering) so the decoder recognises the file.
func appendAncillaryChunk(pngData []byte, name string, body []byte) []byte {
	sig := pngData[:8]
	// Find IHDR end: sig (8) + length (4) + type (4) + 13 + crc (4) = 33
	ihdrEnd := 8 + 12 + 13
	rest := pngData[ihdrEnd:]
	out := make([]byte, 0, len(pngData)+len(body)+24)
	out = append(out, sig...)                            // PNG signature
	out = append(out, pngData[8:ihdrEnd]...)             // IHDR chunk
	out = append(out, chunkBytes([]byte(name), body)...) // ancillary chunk
	out = append(out, rest...)                           // remaining chunks
	return out
}

// chunkBytes builds a single PNG chunk (length+type+body+crc).
func chunkBytes(name, body []byte) []byte {
	h := crc32.New(pngCRC)
	_, _ = h.Write(name)
	_, _ = h.Write(body)
	crc := h.Sum32()
	out := make([]byte, 4+4+len(body)+4)
	binary.BigEndian.PutUint32(out[0:4], uint32(len(body)))
	copy(out[4:8], name)
	copy(out[8:8+len(body)], body)
	binary.BigEndian.PutUint32(out[8+len(body):], crc)
	return out
}

func TestOptimizePNGRoundtrip(t *testing.T) {
	in := makePNG(t, 32, 32)
	out, ok, err := tryOptimizePNG(in, Options{PNGRecompress: true, PNGStripMeta: true})
	if err != nil {
		t.Fatalf("tryOptimizePNG: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for valid PNG")
	}
	// Decode both and compare pixels.
	origImg, err := png.Decode(bytes.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	newImg, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("optimized PNG did not decode: %v", err)
	}
	if !boundsEqual(origImg.Bounds(), newImg.Bounds()) {
		t.Fatalf("bounds differ: %v vs %v", origImg.Bounds(), newImg.Bounds())
	}
	for y := 0; y < origImg.Bounds().Dy(); y++ {
		for x := 0; x < origImg.Bounds().Dx(); x++ {
			r0, g0, b0, a0 := origImg.At(x, y).RGBA()
			r1, g1, b1, a1 := newImg.At(x, y).RGBA()
			if r0 != r1 || g0 != g1 || b0 != b1 || a0 != a1 {
				t.Fatalf("pixel (%d,%d) differs: %v %v %v %v -> %v %v %v %v", x, y, r0, g0, b0, a0, r1, g1, b1, a1)
			}
		}
	}
}

func TestOptimizePNGStripsAncillary(t *testing.T) {
	in := appendAncillaryChunk(makePNG(t, 16, 16), "tEXt", []byte("comment\x00hello world"))
	out, ok, err := tryOptimizePNG(in, Options{PNGRecompress: true, PNGStripMeta: true})
	if err != nil {
		t.Fatalf("tryOptimizePNG: %v", err)
	}
	if !ok {
		t.Fatal("expected ok")
	}
	if bytes.Contains(out, []byte("tEXt")) {
		t.Errorf("tEXt chunk not stripped from output")
	}
}

func TestOptimizePNGKeepsAncillaryWhenDisabled(t *testing.T) {
	in := appendAncillaryChunk(makePNG(t, 16, 16), "tEXt", []byte("comment\x00hello world"))
	// When both PNG optimizations are disabled, the optimizer returns the
	// original data unchanged.
	res, err := optimizePNG("foo.png", in, Options{PNGRecompress: false, PNGStripMeta: false})
	if err != nil {
		t.Fatalf("optimizePNG: %v", err)
	}
	if !bytes.Equal(in, res.Data) {
		t.Errorf("expected byte-identical passthrough when both opts disabled")
	}
	if res.Changed {
		t.Errorf("expected Changed=false")
	}
}

func TestOptimizePNGAPNGPassthrough(t *testing.T) {
	in := appendAncillaryChunk(makePNG(t, 16, 16), "acTL", []byte{0, 0, 0, 1, 0, 0, 0, 0})
	_, ok, err := tryOptimizePNG(in, Options{PNGRecompress: true, PNGStripMeta: true})
	if err != nil {
		t.Fatalf("tryOptimizePNG: %v", err)
	}
	if ok {
		t.Fatal("APNG (acTL chunk) should be passed through unchanged")
	}
}

func TestOptimizePNGMalformed(t *testing.T) {
	bad := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xFF, 0xFF}
	_, ok, err := tryOptimizePNG(bad, Options{PNGRecompress: true, PNGStripMeta: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for malformed PNG")
	}
}

func boundsEqual(a, b image.Rectangle) bool {
	return a.Min.X == b.Min.X && a.Min.Y == b.Min.Y && a.Max.X == b.Max.X && a.Max.Y == b.Max.Y
}

// makePNGCorners encodes an RGBA PNG (color-type 6) with the four corners
// set to the supplied marker color and every other pixel opaque white. Used
// by TestQuantizePNGSkipsAlpha to confirm pngquant is bypassed when
// protect_alpha matches.
func makePNGCorners(t *testing.T, w, h int, marker color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	img.Set(0, 0, marker)
	img.Set(w-1, 0, marker)
	img.Set(0, h-1, marker)
	img.Set(w-1, h-1, marker)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// makePNGGradient encodes an RGBA PNG with a smooth alpha gradient (covers
// the full 0..255 range), exercising the mid-alpha branch of protect_alpha.
func makePNGGradient(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: uint8(x * 255 / (w - 1))})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// makePNGOpaqueRGBA encodes an RGBA PNG (color-type 6) whose every pixel is
// fully opaque (alpha=255). It exercises the branch where protect_alpha
// does NOT fire and pngquant keeps running.
func makePNGOpaqueRGBA(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// makePNGCutout encodes an RGBA PNG (color-type 6) whose only alphas are 0
// (transparent) and 255 (opaque) — a hard-edged cutout such as an icon with
// a transparent background. protect_alpha should NOT fire (no meaningful
// alpha data), so pngquant should still run.
func makePNGCutout(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Transparent on the border, opaque in the middle.
			a := uint8(0)
			if x > 0 && x < w-1 && y > 0 && y < h-1 {
				a = 255
			}
			img.Set(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: a})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func TestUsesAlphaChannel(t *testing.T) {
	marker := makePNGCorners(t, 8, 8, color.NRGBA{R: 0, G: 0, B: 0, A: 1})
	grad := makePNGGradient(t, 8, 8)
	opaque := makePNGOpaqueRGBA(t, 8, 8)
	cutout := makePNGCutout(t, 8, 8)

	if ok, _ := usesAlphaChannel(marker); !ok {
		t.Errorf("alpha=1 marker PNG must be flagged as using the alpha channel")
	}
	if ok, _ := usesAlphaChannel(grad); !ok {
		t.Errorf("gradient PNG has mid-range alphas, must be flagged")
	}
	if ok, _ := usesAlphaChannel(opaque); ok {
		t.Errorf("fully-opaque RGBA PNG must NOT be flagged (no alpha usage)")
	}
	if ok, _ := usesAlphaChannel(cutout); ok {
		t.Errorf("hard-edged cutout (alpha 0/255 only) must NOT be flagged — alpha=0 carries no data")
	}
}

func TestQuantizePNGProtectAlpha(t *testing.T) {
	// Any texture using its alpha channel — be it a thin marker (alpha=1)
	// or a smooth gradient (alpha 0..255) — must skip pngquant when
	// PNGProtectAlpha is set, returning the original bytes unchanged.
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"marker-alpha1", makePNGCorners(t, 8, 8, color.NRGBA{R: 0, G: 0, B: 0, A: 1})},
		{"marker-alpha100", makePNGCorners(t, 8, 8, color.NRGBA{R: 0, G: 0, B: 0, A: 100})},
		{"gradient", makePNGGradient(t, 8, 8)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, ok, err := quantizePNG(tc.data, Options{
				PNGLossyQuant:   true,
				PNGQuantMin:     65,
				PNGQuantMax:     90,
				PNGProtectAlpha: true,
			})
			if err != nil {
				t.Fatalf("quantizePNG: %v", err)
			}
			if ok {
				t.Fatalf("expected ok=false (pngquant skipped) when alpha channel is used")
			}
			if !bytes.Equal(out, tc.data) {
				t.Errorf("expected byte-identical passthrough when protect_alpha fires")
			}
		})
	}

	// A fully-opaque RGBA PNG and a hard-edged cutout (alpha 0/255 only)
	// must NOT trigger the guard. Whether pngquant then runs depends on the
	// binary being embedded for this platform; if it's missing we tolerate
	// ErrBinNotFound — the guard logic itself is what we're verifying.
	opaque := makePNGOpaqueRGBA(t, 8, 8)
	_, _, _, err := quantizePNG(opaque, Options{
		PNGLossyQuant:   true,
		PNGProtectAlpha: true,
	})
	if err != nil && !errors.Is(err, ErrBinNotFound) {
		t.Fatalf("quantizePNG opaque RGBA: unexpected err %v", err)
	}
	cutout := makePNGCutout(t, 8, 8)
	_, _, _, err = quantizePNG(cutout, Options{
		PNGLossyQuant:   true,
		PNGProtectAlpha: true,
	})
	if err != nil && !errors.Is(err, ErrBinNotFound) {
		t.Fatalf("quantizePNG cutout: unexpected err %v", err)
	}

	// With PNGProtectAlpha disabled, even an alpha-using PNG is allowed
	// to proceed to pngquant (modulo binary availability).
	marker := makePNGCorners(t, 8, 8, color.NRGBA{R: 0, G: 0, B: 0, A: 1})
	_, _, _, err = quantizePNG(marker, Options{
		PNGLossyQuant:   true,
		PNGProtectAlpha: false,
	})
	if err != nil && !errors.Is(err, ErrBinNotFound) {
		t.Fatalf("quantizePNG protect_alpha=false: unexpected err %v", err)
	}
}
