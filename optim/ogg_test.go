package optim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// vorbisFormat reads the channels and sample rate from the Vorbis
// identification header on an Ogg stream's first page.
func vorbisFormat(t *testing.T, data []byte) (channels int, rate uint32) {
	t.Helper()
	if !bytes.HasPrefix(data, oggMagic) || len(data) < 27 {
		t.Fatal("not an Ogg stream")
	}
	head := data[27+int(data[26]):]
	if len(head) < 16 || string(head[1:7]) != "vorbis" {
		t.Fatal("no Vorbis identification header")
	}
	return int(head[11]), binary.LittleEndian.Uint32(head[12:16])
}

func requireTool(t *testing.T, tool string) {
	t.Helper()
	if _, err := extractBin(tool); errors.Is(err, ErrBinNotFound) {
		t.Skipf("no embedded %s for this platform", tool)
	}
}

func TestOGGLossyReencode(t *testing.T) {
	requireTool(t, "oggshrink")
	in, err := os.ReadFile("testdata/96khz.ogg") // 0.6 s, mono, 96 kHz, Vorbis q9
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{OGG: OGGOptions{Optimize: true, StripComments: true, Lossy: true, Quality: 4}}
	res, err := optimizeOGG("a.ogg", in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Note != "" {
		t.Fatalf("note: %s", res.Note)
	}
	if !res.Changed || len(res.Data)*100 > len(in)*(100-lossyMinSaving) {
		t.Fatalf("expected a re-encode at least %d%% smaller: %d -> %d bytes", lossyMinSaving, len(in), len(res.Data))
	}
	if ch, rate := vorbisFormat(t, res.Data); ch != 1 || rate != 48000 {
		t.Errorf("got %d channel(s) at %d Hz, want mono at 48000 Hz", ch, rate)
	}

	again, _ := optimizeOGG("a.ogg", in, opts)
	if !bytes.Equal(again.Data, res.Data) {
		t.Error("two re-encodes of the same file differ")
	}
}

func TestOGGLosslessByDefault(t *testing.T) {
	in, err := os.ReadFile("testdata/96khz.ogg")
	if err != nil {
		t.Fatal(err)
	}
	res, err := optimizeOGG("a.ogg", in, Options{OGG: OGGOptions{Optimize: true, StripComments: true, Quality: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if _, rate := vorbisFormat(t, res.Data); rate != 96000 {
		t.Errorf("lossless pass changed the sample rate to %d", rate)
	}
}
