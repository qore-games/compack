package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// tempPrefix names compack's temp files and folders (the extracted tools in
// the temp dir, the ZIP being written next to the output) after the process,
// so the next run can tell the leftovers of a killed one (SIGKILL, e.g. the
// desktop app's Stop) from those of a run still going.
func tempPrefix() string {
	return fmt.Sprintf("compack-%d-", os.Getpid())
}

// removeStale deletes the compack temp entries in dir whose process is gone.
func removeStale(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), "compack-")
		if !ok {
			continue
		}
		pidText, _, ok := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidText)
		if !ok || err != nil || pid == os.Getpid() || processAlive(pid) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}
