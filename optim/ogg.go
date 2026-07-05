package optim

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// oggMagic is the "OggS" captivation bytes every Ogg container starts with.
var oggMagic = []byte("OggS")

// optimizeOGG runs the embedded OptiVorbis binary against an Ogg Vorbis file.
// OptiVorbis is a lossless optimizer: the decoded audio samples are preserved
// exactly and only the encapsulation (Ogg pages, Vorbis headers, comment
// fields, stream serials) is rewritten to be smaller and more reproducible.
//
// Defaults strip the user comment fields (artist/title/etc.) and empty the
// Vorbis vendor string, and bind stream serials to a deterministic value so
// two runs over the same input produce byte-identical output. Pass
// -ogg-keep-comments to retain the user comment fields verbatim.
//
// When no embedded binary exists for the current GOOS/GOARCH, OptiVorbis fails
// for any reason, or the output is no smaller than the input, the original
// bytes are stored unchanged so the build never fails because of one bad
// file.
func optimizeOGG(path string, data []byte, opts Options) (Result, error) {
	res := Result{Data: data, Method: zip.Store}
	if !opts.OGGOptimize {
		return res, nil
	}
	if len(data) < 4 || !bytes.HasPrefix(data, oggMagic) {
		return res, nil
	}
	bin, err := extractBin("optivorbis")
	if err != nil {
		if errors.Is(err, ErrBinNotFound) {
			res.Note = "optivorbis: no embedded binary for this platform"
		} else {
			res.Note = "optivorbis: " + err.Error()
		}
		return res, nil
	}

	// OptiVorbis selects its demuxer from the input file's extension, so it
	// needs a real file on disk rather than a stdin pipe.
	dir, err := os.MkdirTemp("", "compack-ogg-")
	if err != nil {
		return res, nil
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.ogg")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		return res, nil
	}

	args := []string{
		"--quiet",
		"--remuxer", "ogg2ogg",
		"--remuxer_option", "randomize_stream_serials=false",
		"--vendor_string_action", "empty",
	}
	if opts.OGGStripComments {
		args = append(args, "--comment_fields_action", "delete")
	} else {
		args = append(args, "--comment_fields_action", "copy")
	}
	args = append(args, in, "-") // - means stdout

	cmd := exec.Command(bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return res, nil
	}
	result := out.Bytes()
	if len(result) == 0 || !bytes.HasPrefix(result, oggMagic) {
		return res, nil
	}
	if len(result) < len(data) {
		res.Data = result
		res.Changed = true
	}
	return res, nil
}
