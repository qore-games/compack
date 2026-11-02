package optim

import (
	"os"
	"testing"
)

// TestMain removes the tools the tests extracted.
func TestMain(m *testing.M) {
	code := m.Run()
	Cleanup()
	os.Exit(code)
}
