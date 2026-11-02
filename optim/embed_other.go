//go:build !((linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && amd64))

package optim

import "embed"

// No embedded tools for this platform (windows/arm64 has no upstream build):
// extractBin returns ErrBinNotFound and the optimizers leave files as they are.
var binFS embed.FS
