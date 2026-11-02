package optim

import "embed"

// Pre-built binaries (oxipng, OptiVorbis, pngquant from upstream, oggshrink
// from tools/oggshrink) in optim/bin. Each build embeds its own platform's
// only (this file's name sets it), so compack is self-contained without
// carrying the other platforms' tools.
//
//go:embed bin/*_windows_amd64.exe
var binFS embed.FS
