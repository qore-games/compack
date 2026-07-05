package optim

import "archive/zip"

// optimizeShader handles .vsh, .fsh, .glsl GLSL source files. We strip both
// line and block comments and trailing whitespace while preserving newlines
// (so preprocessor line-continuation semantics are not affected) and drop blank
// lines. String and char literals are recognized so that comment markers
// inside them are kept verbatim.
func optimizeShader(path string, data []byte, opts Options) (Result, error) {
	if !opts.TextMinify {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	out := minifyGLSL(data)
	r := Result{Data: out, Method: zip.Deflate, Changed: len(out) != len(data)}
	return r, nil
}

// optimizeLang handles legacy Minecraft .lang files (1.12.2 and earlier),
// which are line based with the format key=value and # comments. We drop blank
// lines and comment lines.
func optimizeLang(path string, data []byte, opts Options) (Result, error) {
	if !opts.TextMinify {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	out := keepKeyValueLines(data, '#')
	r := Result{Data: out, Method: zip.Deflate, Changed: len(out) != len(data)}
	return r, nil
}

// optimizeProperties handles OptiFine .properties files. Same shape as .lang
// but accepts both '#' and '!' as comment markers.
func optimizeProperties(path string, data []byte, opts Options) (Result, error) {
	if !opts.TextMinify {
		return Result{Data: data, Method: zip.Deflate}, nil
	}
	out := keepKeyValueLines(data, '#', '!')
	r := Result{Data: out, Method: zip.Deflate, Changed: len(out) != len(data)}
	return r, nil
}

// keepKeyValueLines keeps every non-blank line that contains a key/value
// separator ('='); drops blank and comment lines. Output preserves all kept
// lines verbatim separated by '\n'.
func keepKeyValueLines(data []byte, commentMarkers ...byte) []byte {
	out := make([]byte, 0, len(data))
	start := 0
	flush := func(end int) {
		line := data[start:end]
		// Strip trailing \r.
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			return
		}
		// Comment line?
		for _, m := range commentMarkers {
			if line[0] == m {
				return
			}
		}
		// Must contain '='.
		hasEq := false
		for _, c := range line {
			if c == '=' {
				hasEq = true
				break
			}
		}
		if !hasEq {
			return
		}
		out = append(out, line...)
		out = append(out, '\n')
	}
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			flush(i)
			start = i + 1
		}
	}
	if start < len(data) {
		flush(len(data))
	}
	return out
}

// minifyGLSL removes // and /* */ comments while preserving string ('...'"
// and \"...\" literals), collapses runs of inline whitespace into a single
// space and drops blank lines. Newlines outside comments are preserved.
func minifyGLSL(data []byte) []byte {
	out := make([]byte, 0, len(data))
	n := len(data)
	i := 0
	prevNL := true // suppress leading blank line
	emitNL := func() {
		if !prevNL {
			out = append(out, '\n')
			prevNL = true
		}
	}
	for i < n {
		c := data[i]
		switch {
		case c == '/' && i+1 < n && data[i+1] == '/':
			for i < n && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && data[i+1] == '*':
			i += 2
			for i+1 < n && !(data[i] == '*' && data[i+1] == '/') {
				if data[i] == '\n' {
					emitNL()
				}
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}
		case c == '"' || c == '\'':
			quote := c
			out = append(out, c)
			i++
			for i < n {
				ch := data[i]
				out = append(out, ch)
				if ch == '\\' && i+1 < n {
					out = append(out, data[i+1])
					i += 2
					continue
				}
				i++
				if ch == quote {
					break
				}
			}
			prevNL = false
		case c == '\n':
			emitNL()
			i++
		case c == ' ' || c == '\t' || c == '\r':
			if !prevNL && len(out) > 0 && out[len(out)-1] != ' ' && out[len(out)-1] != '\n' {
				out = append(out, ' ')
			}
			i++
		default:
			out = append(out, c)
			i++
			prevNL = false
		}
	}
	// Trim a trailing space if any.
	if len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out
}
