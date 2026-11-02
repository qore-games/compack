package main

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/qore-games/compack/optim"
)

func TestCacheReusesSlowResults(t *testing.T) {
	calls := map[string]int{}
	spec := optim.Spec{Kind: "TEST", Slow: true, Optimize: func(_ string, data []byte, _ optim.Options) (optim.Result, error) {
		calls[string(data)]++
		switch {
		case bytes.HasPrefix(data, []byte("keep")):
			return optim.Result{Data: data, Method: zip.Deflate}, nil
		case bytes.HasPrefix(data, []byte("fail")):
			return optim.Result{Data: data, Method: zip.Store, Note: "tool failed"}, nil
		}
		return optim.Result{Data: bytes.ToUpper(data), Method: zip.Store, Changed: true}, nil
	}}

	dir := t.TempDir()
	opts := defaultConfig().Options
	c, err := openCache(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"shrink me", "keep me", "fail me"} {
		first, _ := c.optimize(spec, "a.x", []byte(in), opts)
		again, _ := c.optimize(spec, "a.x", []byte(in), opts)
		if !bytes.Equal(first.Data, again.Data) || first.Method != again.Method || first.Changed != again.Changed || first.Note != again.Note {
			t.Errorf("%q: cached result %+v differs from %+v", in, again, first)
		}
	}
	if calls["shrink me"] != 1 || calls["keep me"] != 1 {
		t.Errorf("results should come from the cache the second time: %v", calls)
	}
	if calls["fail me"] != 2 {
		t.Errorf("a result with a note (a tool failed) must not be cached: %v", calls)
	}

	// Other options never reuse these entries.
	opts.PNG.Lossy = false
	other, err := openCache(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.optimize(spec, "a.x", []byte("shrink me"), opts); err != nil {
		t.Fatal(err)
	}
	if calls["shrink me"] != 2 {
		t.Errorf("a change of options must miss the cache: %v", calls)
	}
}
