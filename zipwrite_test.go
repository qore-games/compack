package main

import (
	"archive/zip"
	"bytes"
	"hash/crc32"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
)

func testEntries(t *testing.T) ([]entry, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{
		"pack.mcmeta":                   []byte(`{"pack":{"pack_format":46,"description":"test"}}`),
		"assets/a/models/one.json":      []byte(strings.Repeat(`{"parent":"item/generated"}`, 20)),
		"assets/a/models/two.json":      []byte(strings.Repeat(`{"parent":"item/generated"}`, 20)), // duplicate of one.json
		"assets/a/textures/tiny.txt":    []byte("x"),                                               // deflate is bigger: stored
		"assets/a/sounds/é.ogg":         []byte("OggS not really audio"),                           // non-ASCII name, stored
		"assets/a/sounds/copy-of-é.ogg": []byte("OggS not really audio"),                           // stored duplicate
		"assets/a/textures/empty.png":   {},
	}
	c := newCompressor()
	var entries []entry
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		data := files[rel]
		method := uint16(zip.Deflate)
		if strings.HasSuffix(rel, ".ogg") {
			method = zip.Store
		}
		e := entry{rel: rel, size: int64(len(data)), crc: crc32.ChecksumIEEE(data)}
		e.method, e.payload = c.pack(data, method)
		entries = append(entries, e)
	}
	return entries, files
}

func TestWriteZipRoundTrip(t *testing.T) {
	for _, localNames := range []bool{false, true} {
		entries, files := testEntries(t)
		var buf bytes.Buffer
		size, dedup, err := writeZip(&buf, entries, localNames)
		if err != nil {
			t.Fatal(err)
		}
		if size != int64(buf.Len()) {
			t.Errorf("localNames=%v: reported size %d, wrote %d", localNames, size, buf.Len())
		}
		if dedup == 0 {
			t.Errorf("localNames=%v: expected the duplicates to share bodies", localNames)
		}

		// archive/zip, like java.util.zip.ZipFile, reads names from the
		// central directory and skips the local name.
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatalf("localNames=%v: %v", localNames, err)
		}
		if len(zr.File) != len(files) {
			t.Fatalf("localNames=%v: %d entries, want %d", localNames, len(zr.File), len(files))
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if !bytes.Equal(got, files[f.Name]) {
				t.Errorf("localNames=%v: %s: got %q, want %q", localNames, f.Name, got, files[f.Name])
			}
		}
	}
}

func TestPackStoresWhenDeflateIsNotSmaller(t *testing.T) {
	c := newCompressor()
	if m, p := c.pack([]byte("x"), zip.Deflate); m != zip.Store || string(p) != "x" {
		t.Errorf("1-byte file: method %d payload %q, want stored as is", m, p)
	}
	data := []byte(strings.Repeat("minecraft:", 100))
	if m, p := c.pack(data, zip.Deflate); m != zip.Deflate || len(p) >= len(data) {
		t.Errorf("repetitive file: method %d, %d bytes, want deflated and smaller", m, len(p))
	}
	if m, _ := c.pack(data, zip.Store); m != zip.Store {
		t.Errorf("an optimizer asking for Store must get Store")
	}
}
