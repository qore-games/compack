package optim

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Pre-built upstream binaries (oxipng + OptiVorbis) for every GOOS/GOARCH
// combination that has an upstream release. Embedding them turns compack into a
// self-contained binary with no runtime binary dependency on $PATH for PNG and
// OGG optimization. windows/arm64 has no upstream release for either tool, so
// it is intentionally absent; callers see ErrBinNotFound on that platform.
//
//go:embed bin
var binFS embed.FS

// ErrBinNotFound is returned by extractBin when no embedded binary exists for
// the current GOOS/GOARCH (e.g. windows/arm64 has no upstream release).
var ErrBinNotFound = errors.New("optim: no embedded binary for this GOOS/GOARCH")

var (
	binMu    sync.Mutex
	binCache = make(map[string]string) // tool name -> extracted filesystem path
)

// binAssetName returns the in-tree path of the embedded binary for the named
// tool ("oxipng" or "optivorbis") targeting the current GOOS/GOARCH. Returns
// ErrBinNotFound when the platform is unsupported.
func binAssetName(tool string) (string, error) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	rel := fmt.Sprintf("bin/%s_%s_%s%s", tool, runtime.GOOS, runtime.GOARCH, ext)
	if _, err := fs.Stat(binFS, rel); err != nil {
		return "", ErrBinNotFound
	}
	return rel, nil
}

// extractBin extracts the embedded binary for the named tool into a
// process-scoped temp directory and returns its filesystem path. The path is
// cached for the rest of the process so repeat calls do not re-extract. On
// non-windows targets the file is made executable. On an unsupported platform
// returns ErrBinNotFound without touching the filesystem.
func extractBin(tool string) (string, error) {
	binMu.Lock()
	defer binMu.Unlock()
	if p, ok := binCache[tool]; ok {
		return p, nil
	}
	rel, err := binAssetName(tool)
	if err != nil {
		return "", err
	}
	raw, err := fs.ReadFile(binFS, rel)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "compack-bin-")
	if err != nil {
		return "", err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	path := filepath.Join(dir, tool+ext)
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o755)
	}
	binCache[tool] = path
	return path, nil
}
