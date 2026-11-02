package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/qore-games/compack/optim"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

// reporter receives a run's progress and outcome: textReporter draws the
// progress bar and log lines on stderr, eventReporter prints JSON lines on
// stdout (-events). progress is called from one goroutine, in completion
// order; file and note once per file afterwards, in path order.
type reporter interface {
	start(total int)
	progress(done, total int, rel string)
	file(e entry)
	note(rel, note string)
	done(s summary)
	fail(err error)
}

// newReporter picks the reporter for cfg; events go to stdout.
func newReporter(cfg config, stdout io.Writer) reporter {
	if cfg.Events {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		return &eventReporter{enc: enc}
	}
	return &textReporter{cfg: cfg}
}

type textReporter struct {
	cfg config
	bar *progressbar.ProgressBar
}

func (r *textReporter) start(total int) {
	// Live progress bar drawn on stderr while files are optimized, only when
	// stderr is a TTY (so output captured to a file or piped through a tool
	// stays clean). Suppressed by -q, -v, -dry-run or -no-progress.
	c := r.cfg
	if c.Quiet || c.Verbose || c.DryRun || c.NoProgress || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	r.bar = progressbar.NewOptions(total,
		progressbar.OptionSetDescription("Optimizing pack"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("files"),
		progressbar.OptionSetWidth(40),
		progressbar.OptionThrottle(60*time.Millisecond),
		progressbar.OptionOnCompletion(func() { fmt.Fprintln(os.Stderr) }),
		progressbar.OptionShowDescriptionAtLineEnd(),
	)
}

func (r *textReporter) progress(done, total int, rel string) {
	if r.bar == nil {
		return
	}
	// Show the current file's optimizer type so the user can see at a
	// glance which task the worker pool is busy on right now.
	r.bar.Describe(optim.Lookup(rel).Kind + " " + trimPath(rel))
	_ = r.bar.Add(1)
}

func (r *textReporter) file(e entry) {
	if r.bar != nil {
		return // the bar already showed progress; skip noisy per-file log
	}
	switch {
	case r.cfg.Verbose:
	case r.cfg.Quiet || !e.changed || e.size >= e.orig:
		return
	}
	sign, delta := "=", int64(0)
	if e.size < e.orig {
		sign, delta = "-", e.orig-e.size
	} else if e.size > e.orig {
		sign, delta = "+", e.size-e.orig
	}
	log.Printf("%-60s %s%s  (%d -> %d)", e.rel, sign, humanBytes(delta), e.orig, e.size)
}

func (r *textReporter) note(rel, note string) {
	log.Printf("note: %s: %s", rel, note)
}

func (r *textReporter) done(s summary) {
	red := 0.0
	if s.InputBytes > 0 {
		red = float64(s.InputBytes-s.OutputBytes) / float64(s.InputBytes) * 100
	}
	msg := fmt.Sprintf("%d files, %d optimized", s.Files, s.Optimized)
	if s.DedupBytes > 0 {
		msg += fmt.Sprintf(", %s deduped", humanBytes(s.DedupBytes))
	}
	msg += fmt.Sprintf(", %s -> %s (%.1f%% smaller) in %s",
		humanBytes(s.InputBytes), humanBytes(s.OutputBytes), red, time.Duration(s.Millis)*time.Millisecond)
	if s.Out == "" {
		log.Printf("dry run: %s, nothing written", msg)
		return
	}
	log.Printf("done: %s", msg)
	log.Printf("wrote %s (sha1 %s)", s.Out, s.SHA1)
}

func (r *textReporter) fail(error) {} // main prints the error

// eventReporter prints one JSON object per line on stdout:
//
//	{"event":"start","total":62318}
//	{"event":"progress","done":120,"total":62318,"kind":"PNG","file":"assets/…"}
//	{"event":"note","file":"assets/…","note":"…"}
//	{"event":"done","files":62318,"optimized":10322,…,"sha1":"…"}
//	{"event":"error","message":"…"}
//
// progress events are throttled to one every 100 ms (plus the last one).
type eventReporter struct {
	enc  *json.Encoder
	last time.Time
}

func (r *eventReporter) emit(v any) { _ = r.enc.Encode(v) }

func (r *eventReporter) start(total int) {
	r.emit(struct {
		Event string `json:"event"`
		Total int    `json:"total"`
	}{"start", total})
}

func (r *eventReporter) progress(done, total int, rel string) {
	if done < total && time.Since(r.last) < 100*time.Millisecond {
		return
	}
	r.last = time.Now()
	r.emit(struct {
		Event string `json:"event"`
		Done  int    `json:"done"`
		Total int    `json:"total"`
		Kind  string `json:"kind"`
		File  string `json:"file"`
	}{"progress", done, total, optim.Lookup(rel).Kind, rel})
}

func (r *eventReporter) file(entry) {}

func (r *eventReporter) note(rel, note string) {
	r.emit(struct {
		Event string `json:"event"`
		File  string `json:"file"`
		Note  string `json:"note"`
	}{"note", rel, note})
}

func (r *eventReporter) done(s summary) {
	r.emit(struct {
		Event string `json:"event"`
		summary
	}{"done", s})
}

func (r *eventReporter) fail(err error) {
	r.emit(struct {
		Event   string `json:"event"`
		Message string `json:"message"`
	}{"error", err.Error()})
}

// trimPath shortens a long relative path so the progress-bar description stays
// on a single terminal line; keeps the trailing portion (which usually
// identifies the asset) and prefixes an ellipsis when truncated.
func trimPath(rel string) string {
	const max = 48
	if len(rel) <= max {
		return rel
	}
	return "..." + rel[len(rel)-(max-3):]
}
