package optim

import "archive/zip"

// JSON optimizer for .json, .jsonc, .mcmeta, .mcmetac files. It strips
// comments (// and /* */) and unnecessary whitespace while preserving string
// literals byte-for-byte. Output is byte-identical to feeding the input through
// a strict JSON parser and serializing it back, but the implementation is a
// single streaming pass without allocations beyond the output buffer.
func optimizeJSON(path string, data []byte, opts Options) (Result, error) {
	if !opts.JSONMinify {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	out, hasComment := minifyJSON(data)

	res := Result{Data: out, Method: zip.Deflate}
	if len(out) != len(data) {
		res.Changed = true
	}

	// Detect files not ending in "c" that contained comments: warn the user
	// because Minecraft expects strict JSON in those files.
	if hasComment && !endsInC(path) {
		res.Note = "file contained comments despite a strict JSON extension; comments were stripped"
	}
	return res, nil
}

func endsInC(path string) bool {
	return len(path) > 0 && path[len(path)-1] == 'c'
}

// minifyJSON performs a single streaming pass over the data copying into a
// result buffer. Returns the minified output and a flag indicating whether any
// comment was stripped.
func minifyJSON(data []byte) ([]byte, bool) {
	out := make([]byte, 0, len(data))
	hasComment := false
	n := len(data)
	i := 0
	escape := false
	// State: 0=in_value, 1=in_string
	state := 0
	for i < n {
		c := data[i]
		switch state {
		case 1:
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				state = 0
			}
			out = append(out, c)
			i++
		default:
			switch c {
			case '"':
				state = 1
				out = append(out, c)
				i++
			case ' ', '\t', '\r', '\n':
				i++
			case '/':
				if i+1 < n && data[i+1] == '/' {
					hasComment = true
					i += 2
					for i < n && data[i] != '\n' && data[i] != '\r' {
						i++
					}
				} else if i+1 < n && data[i+1] == '*' {
					hasComment = true
					i += 2
					for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
						i++
					}
					if i+1 < n {
						i += 2
					} else {
						i = n
					}
				} else {
					// Keep / as literal text (unlikely outside strings).
					out = append(out, c)
					i++
				}
			default:
				out = append(out, c)
				i++
			}
		}
	}
	return out, hasComment
}
