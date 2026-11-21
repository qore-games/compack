package optim

import (
	"regexp"
	"strings"
	"sync"
)

// globs keeps each pattern's compiled form: the same few patterns are
// matched against every file of a pack.
var globs sync.Map // pattern -> *regexp.Regexp

// matchGlob tells whether path (`/`-separated, relative to the pack's root)
// matches pattern: `*` matches within one name, `**` across directories
// (`a/**/b` also matches `a/b`), `?` one character; the rest is literal.
func matchGlob(pattern, path string) bool {
	re, ok := globs.Load(pattern)
	if !ok {
		re, _ = globs.LoadOrStore(pattern, compileGlob(pattern))
	}
	return re.(*regexp.Regexp).MatchString(path)
}

func compileGlob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			i++
			if i+1 < len(pattern) && pattern[i+1] == '/' {
				// `**/`: any directories, none included
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			// the byte itself: a name's non-ASCII bytes stay as they are
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
