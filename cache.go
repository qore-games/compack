package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/qore-games/compack/optim"
)

// resultCache keeps the results of the slow optimizers (PNG, OGG: the ones
// running an external tool) by content, so a rerun on a pack where most files
// did not change only optimizes the changed ones (-cache <dir>, off by
// default). An entry is keyed by the file's bytes, its kind, the options and
// the embedded tools: changing any of them misses. Entries are never pruned;
// point -cache at a folder you can wipe (a CI cache, a build dir).
type resultCache struct {
	dir  string
	salt []byte
}

// cacheFormat is bumped when an entry's layout changes.
const cacheFormat = "compack-cache-1"

func openCache(dir string, opts optim.Options) (*resultCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	salt := fmt.Sprintf("%s\n%s\n%+v\n", cacheFormat, optim.ToolsFingerprint(), opts)
	return &resultCache{dir: dir, salt: []byte(salt)}, nil
}

// optimize returns the cached result for data, or runs spec and stores its
// result. Results carrying a note (a tool failed) are not stored, so the
// next run tries again.
func (c *resultCache) optimize(spec optim.Spec, rel string, data []byte, opts optim.Options) (optim.Result, error) {
	h := sha256.New()
	h.Write(c.salt)
	h.Write([]byte(spec.Kind))
	h.Write(data)
	key := hex.EncodeToString(h.Sum(nil))
	path := filepath.Join(c.dir, key[:2], key)

	if r, ok := c.load(path, data); ok {
		return r, nil
	}
	r, err := spec.Optimize(rel, data, opts)
	if err == nil && r.Note == "" {
		c.store(path, r)
	}
	return r, err
}

// An entry is the ZIP method (2 bytes, little endian) followed by the
// optimized bytes, or by nothing when the optimizer kept the input as is.
func (c *resultCache) load(path string, data []byte) (optim.Result, bool) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) < 2 {
		return optim.Result{}, false
	}
	r := optim.Result{Method: binary.LittleEndian.Uint16(raw), Data: data}
	if len(raw) > 2 {
		r.Data, r.Changed = raw[2:], true
	}
	return r, true
}

// store writes an entry atomically (temp file + rename), so concurrent runs
// sharing a cache never read half an entry. Failing to store is not an error:
// the cache is only a shortcut.
func (c *resultCache) store(path string, r optim.Result) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	var method [2]byte
	binary.LittleEndian.PutUint16(method[:], r.Method)
	_, err = f.Write(method[:])
	if err == nil && r.Changed {
		_, err = f.Write(r.Data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
}
