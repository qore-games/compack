package optim

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// oggMagic is the "OggS" capture pattern every Ogg page starts with.
var oggMagic = []byte("OggS")

// lossyMinSaving is how much smaller (in percent) a re-encoded OGG must be to
// replace the original: files already encoded at about the target quality
// are left alone rather than re-encoded for a few bytes.
const lossyMinSaving = 15

// optimizeOGG shrinks an Ogg Vorbis file in up to two steps:
//
//  1. (opt-in, lossy) the embedded oggshrink re-encodes it at OGG.Quality,
//     halving sample rates above 48 kHz; the result is kept only when it is
//     at least lossyMinSaving percent smaller.
//  2. OptiVorbis, lossless: the decoded samples are preserved exactly and
//     only the encapsulation (Ogg pages, Vorbis headers, comment fields,
//     stream serials) is rewritten to be smaller and more reproducible.
//
// Defaults strip the user comment fields (artist/title/etc.) and empty the
// Vorbis vendor string, and bind stream serials to a deterministic value so
// two runs over the same input produce byte-identical output. Pass
// -ogg-strip-comments=false to retain the user comment fields verbatim.
//
// When no embedded binary exists for the current GOOS/GOARCH, a tool fails
// for any reason, or the output is no smaller than the input, the original
// bytes are stored unchanged so the build never fails because of one bad
// file.
func optimizeOGG(path string, data []byte, opts Options) (Result, error) {
	res := Result{Data: data, Method: zip.Store}
	if !opts.OGG.Optimize && !opts.OGG.Lossy {
		return res, nil
	}
	if !bytes.HasPrefix(data, oggMagic) {
		return res, nil
	}

	best := data
	if opts.OGG.Lossy {
		out, err := reencodeOGG(data, opts.OGG.Quality)
		if err != nil && !errors.Is(err, ErrBinNotFound) {
			res.Note = "oggshrink failed: " + err.Error()
		}
		if bytes.HasPrefix(out, oggMagic) && len(out)*100 <= len(data)*(100-lossyMinSaving) {
			best = out
		}
	}
	if opts.OGG.Optimize {
		out, note := runOptiVorbis(best, opts.OGG)
		if note != "" {
			res.Note = note
		}
		if len(out) > 0 && len(out) < len(best) {
			best = out
		}
	}
	if len(best) < len(data) {
		res.Data = best
		res.Changed = true
	}
	return res, nil
}

// reencodeOGG runs the embedded oggshrink (libvorbis) at the given Vorbis
// quality (0-10).
func reencodeOGG(data []byte, quality int) ([]byte, error) {
	bin, err := extractBin("oggshrink")
	if err != nil {
		return nil, err
	}
	return runTool(bin, []string{strconv.Itoa(min(max(quality, 0), 10))}, data)
}

// runOptiVorbis losslessly repackages an Ogg Vorbis file. It returns nil (and
// a note when something failed) when it could not.
func runOptiVorbis(data []byte, opts OGGOptions) ([]byte, string) {
	bin, err := extractBin("optivorbis")
	if errors.Is(err, ErrBinNotFound) {
		return nil, "optivorbis: no embedded binary for this platform"
	} else if err != nil {
		return nil, "optivorbis: " + err.Error()
	}

	// OptiVorbis selects its demuxer from the input file's extension, so it
	// needs a real file on disk rather than a stdin pipe.
	f, err := CreateTemp("*.ogg")
	if err != nil {
		return nil, ""
	}
	in := f.Name()
	defer os.Remove(in)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, ""
	}

	args := []string{
		"--quiet",
		"--remuxer", "ogg2ogg",
		"--remuxer_option", "randomize_stream_serials=false",
		"--vendor_string_action", "empty",
	}
	if opts.StripComments {
		args = append(args, "--comment_fields_action", "delete")
	} else {
		args = append(args, "--comment_fields_action", "copy")
	}
	args = append(args, in, "-") // - means stdout

	cmd := exec.Command(bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := runCmd(cmd); err != nil {
		return nil, "optivorbis failed: " + toolMessage(errb.String(), err)
	}
	if !bytes.HasPrefix(out.Bytes(), oggMagic) {
		return nil, ""
	}
	return out.Bytes(), ""
}
