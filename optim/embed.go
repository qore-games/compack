package optim

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// ErrBinNotFound is returned by extractBin when no embedded binary exists for
// the current GOOS/GOARCH (e.g. windows/arm64 has no upstream release).
var ErrBinNotFound = errors.New("optim: no embedded binary for this GOOS/GOARCH")

// TempPrefix names the process-scoped temp directory (compack sets it to a
// name carrying its pid, so a later run can clean up after a killed one).
var TempPrefix = "compack-"

var (
	binMu    sync.Mutex
	workDir  string                   // process-scoped temp dir: extracted tools + scratch files
	binCache = map[string]string{}    // tool name -> extracted filesystem path
	running  = map[*os.Process]bool{} // tool processes in flight, stopped by Cleanup
)

// binAssetName returns the in-tree path of the embedded binary for the named
// tool ("oxipng", "optivorbis", "pngquant" or "oggshrink") targeting the current
// GOOS/GOARCH. Returns ErrBinNotFound when the platform is unsupported.
func binAssetName(tool string) (string, error) {
	rel := fmt.Sprintf("bin/%s_%s_%s%s", tool, runtime.GOOS, runtime.GOARCH, exeExt())
	if _, err := fs.Stat(binFS, rel); err != nil {
		return "", ErrBinNotFound
	}
	return rel, nil
}

func exeExt() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// scratchDir returns the process-scoped temp directory, creating it on first
// use. Cleanup removes it. Callers hold binMu.
func scratchDir() (string, error) {
	if workDir == "" {
		dir, err := os.MkdirTemp("", TempPrefix)
		if err != nil {
			return "", err
		}
		workDir = dir
	}
	return workDir, nil
}

// CreateTemp creates a scratch file in the process-scoped temp directory.
func CreateTemp(pattern string) (*os.File, error) {
	binMu.Lock()
	dir, err := scratchDir()
	binMu.Unlock()
	if err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, pattern)
}

// Cleanup stops the tools still running and removes the extracted tools and
// scratch files. Call it once the optimizers are done (and on interrupt).
func Cleanup() {
	binMu.Lock()
	defer binMu.Unlock()
	for p := range running {
		_ = p.Kill()
	}
	if workDir != "" {
		_ = os.RemoveAll(workDir)
		workDir = ""
		clear(binCache)
	}
}

// runCmd runs a tool, tracked so Cleanup can stop it.
func runCmd(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	binMu.Lock()
	running[cmd.Process] = true
	binMu.Unlock()
	err := cmd.Wait()
	binMu.Lock()
	delete(running, cmd.Process)
	binMu.Unlock()
	return err
}

// extractBin extracts the embedded binary for the named tool into the
// process-scoped temp directory and returns its filesystem path. The path is
// cached until Cleanup so repeat calls do not re-extract. On an unsupported
// platform returns ErrBinNotFound without touching the filesystem.
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
	dir, err := scratchDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, tool+exeExt())
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		return "", err
	}
	binCache[tool] = path
	return path, nil
}

// ToolsFingerprint hashes the embedded tools for this platform, so cached
// results are dropped when a tool changes.
func ToolsFingerprint() string {
	h := sha256.New()
	for _, tool := range []string{"oxipng", "optivorbis", "pngquant", "oggshrink"} {
		rel, err := binAssetName(tool)
		if err != nil {
			continue
		}
		raw, err := fs.ReadFile(binFS, rel)
		if err != nil {
			continue
		}
		h.Write([]byte(rel))
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}
