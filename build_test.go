package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// writePack lays out a small pack folder and returns it.
func writePack(t *testing.T, dir string) string {
	t.Helper()
	pack := filepath.Join(dir, "pack")
	ogg, err := os.ReadFile("optim/testdata/96khz.ogg")
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	img.Set(0, 0, color.NRGBA{A: 255})
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"pack.mcmeta":                         []byte("{\n  \"pack\": { \"pack_format\": 46, \"description\": \"test\" }\n}\n"),
		"assets/a/models/item/one.json":       []byte(`{ "parent": "item/generated" }`),
		"assets/a/models/item/two.json":       []byte(`{ "parent": "item/generated" }`),
		"assets/a/shaders/core/x.fsh":         []byte("// comment\nvoid main() {\n\n  gl_FragColor = vec4(1.0);\n}\n"),
		"assets/a/lang/en_us.lang":            []byte("# comment\n\nkey=value\n"),
		"assets/a/sounds/tone.ogg":            ogg,
		"assets/a/textures/item/sword.png":    pngData.Bytes(),
		"credits.txt":                         []byte("kept as is"),
		".git/HEAD":                           []byte("ref: refs/heads/main"), // hidden: left out
		"assets/a/.idea/workspace.xml":        []byte("<x/>"),                 // hidden: left out
		"assets/a/textures/block/stone.png.x": []byte("not a png"),
	}
	for rel, data := range files {
		p := filepath.Join(pack, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return pack
}

type event struct {
	Event string `json:"event"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	Kind  string `json:"kind"`
	summary
}

func readEvents(t *testing.T, r io.Reader) []event {
	t.Helper()
	var out []event
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var e event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("event %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBuildFolder(t *testing.T) {
	dir := t.TempDir()
	pack := writePack(t, dir)
	out := filepath.Join(pack, "pack.zip") // inside the input: a rerun must not pack it
	cfg := defaultConfig()
	cfg.inPath, cfg.Out, cfg.Events, cfg.Threads = pack, out, true, 4

	var events bytes.Buffer
	if err := build(cfg, newReporter(cfg, &events)); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(out); runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Errorf("output mode %v, want 0644", info.Mode().Perm())
	}

	// The -events contract the desktop app reads.
	evs := readEvents(t, &events)
	if len(evs) < 3 || evs[0].Event != "start" || evs[len(evs)-1].Event != "done" {
		t.Fatalf("events: want start ... done, got %+v", evs)
	}
	const files = 9
	if evs[0].Total != files {
		t.Errorf("start total %d, want %d", evs[0].Total, files)
	}
	last := evs[len(evs)-2]
	if last.Event != "progress" || last.Done != files || last.Total != files {
		t.Errorf("the last progress event must report every file: %+v", last)
	}
	done := evs[len(evs)-1].summary
	sum := sha1.Sum(first)
	switch {
	case done.Files != files:
		t.Errorf("done files %d, want %d", done.Files, files)
	case done.Out != out:
		t.Errorf("done out %q, want %q", done.Out, out)
	case done.SHA1 != hex.EncodeToString(sum[:]):
		t.Errorf("done sha1 %s does not match the file", done.SHA1)
	case done.OutputBytes != int64(len(first)):
		t.Errorf("done output_bytes %d, file is %d", done.OutputBytes, len(first))
	case done.DedupBytes == 0:
		t.Error("the two identical models should share a body")
	case done.Optimized == 0 || done.InputBytes == 0:
		t.Errorf("done: %+v", done)
	}

	zr, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(data)
	}
	var names []string
	for name := range got {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{
		"assets/a/lang/en_us.lang", "assets/a/models/item/one.json", "assets/a/models/item/two.json",
		"assets/a/shaders/core/x.fsh", "assets/a/sounds/tone.ogg", "assets/a/textures/block/stone.png.x",
		"assets/a/textures/item/sword.png", "credits.txt", "pack.mcmeta",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("entries:\n got %q\nwant %q", names, want)
	}
	for name, content := range map[string]string{
		"pack.mcmeta":                         `{"pack":{"pack_format":46,"description":"test"}}`,
		"assets/a/models/item/two.json":       `{"parent":"item/generated"}`,
		"assets/a/lang/en_us.lang":            "key=value\n",
		"assets/a/shaders/core/x.fsh":         "void main() {\ngl_FragColor = vec4(1.0);\n}",
		"credits.txt":                         "kept as is",
		"assets/a/textures/block/stone.png.x": "not a png",
	} {
		if got[name] != content {
			t.Errorf("%s: got %q, want %q", name, got[name], content)
		}
	}
	if _, err := png.Decode(bytes.NewReader([]byte(got["assets/a/textures/item/sword.png"]))); err != nil {
		t.Errorf("sword.png does not decode: %v", err)
	}

	// Same input, same bytes; the previous output inside the pack is left out.
	events.Reset()
	if err := build(cfg, newReporter(cfg, &events)); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(out)
	if !bytes.Equal(first, second) {
		t.Error("a second run gave different bytes")
	}

	// A dry run reports the same size and writes nothing.
	if err := os.WriteFile(out, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.DryRun = true
	events.Reset()
	if err := build(cfg, newReporter(cfg, &events)); err != nil {
		t.Fatal(err)
	}
	evs = readEvents(t, &events)
	dry := evs[len(evs)-1].summary
	if dry.OutputBytes != int64(len(first)) || dry.Out != "" || dry.SHA1 != "" {
		t.Errorf("dry run summary %+v", dry)
	}
	if kept, _ := os.ReadFile(out); string(kept) != "previous" {
		t.Error("a dry run wrote its output")
	}
}

func TestBuildZipInput(t *testing.T) {
	dir := t.TempDir()
	pack := writePack(t, dir)
	cfg := defaultConfig()
	cfg.inPath, cfg.Out, cfg.Threads = pack, filepath.Join(dir, "a.zip"), 4
	cfg.Quiet, cfg.NoProgress = true, true
	if err := build(cfg, newReporter(cfg, io.Discard)); err != nil {
		t.Fatal(err)
	}
	// Repacking the output (a zip input) keeps every file as it is.
	cfg.inPath, cfg.Out = cfg.Out, filepath.Join(dir, "b.zip")
	if err := build(cfg, newReporter(cfg, io.Discard)); err != nil {
		t.Fatal(err)
	}
	a, _ := zip.OpenReader(filepath.Join(dir, "a.zip"))
	b, _ := zip.OpenReader(filepath.Join(dir, "b.zip"))
	defer a.Close()
	defer b.Close()
	if len(a.File) != len(b.File) {
		t.Fatalf("%d entries repacked into %d", len(a.File), len(b.File))
	}
	for i := range a.File {
		ra, _ := a.File[i].Open()
		rb, _ := b.File[i].Open()
		da, _ := io.ReadAll(ra)
		db, _ := io.ReadAll(rb)
		ra.Close()
		rb.Close()
		if a.File[i].Name != b.File[i].Name || !bytes.Equal(da, db) {
			t.Errorf("%s changed when repacked", a.File[i].Name)
		}
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.Events = true
	for name, in := range map[string]string{"missing": filepath.Join(dir, "nope"), "not a pack": filepath.Join(dir, "x.txt")} {
		_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644)
		cfg.inPath, cfg.Out = in, filepath.Join(dir, "out.zip")
		var events bytes.Buffer
		rep := newReporter(cfg, &events)
		err := build(cfg, rep)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		rep.fail(err)
		evs := readEvents(t, &events)
		if len(evs) != 1 || evs[0].Event != "error" {
			t.Errorf("%s: want one error event, got %q", name, events.String())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a failed run left files behind: %v", entries)
	}
}
