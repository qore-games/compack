package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoveStale(t *testing.T) {
	// A pid that just exited.
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	dead := cmd.Process.Pid

	dir := t.TempDir()
	keep := []string{
		tempPrefix() + "123.zip.tmp", // this process
		"compack-notapid-1",
		"pack-compack.zip",
	}
	gone := []string{
		fmt.Sprintf("compack-%d-42.zip.tmp", dead),
		fmt.Sprintf("compack-%d-9", dead), // a tools folder
	}
	for _, name := range append(keep, gone...) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removeStale(dir)
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed", name)
		}
	}
	for _, name := range gone {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s (dead pid %d) was kept", name, dead)
		}
	}
}
