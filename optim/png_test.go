package optim

import (
	"bytes"
	"encoding/binary"
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
