package output

import (
	"fmt"
	"io"
	"strings"
)

type Writer interface {
	Step(label, detail string)
	Ok(label, detail string)
	Fail(label, detail string)
	Warn(label, detail string)
	Summary(passed, failed int)
}

func NewText(w io.Writer) Writer { return &textWriter{w: w} }

type textWriter struct {
	w io.Writer
}

func (t *textWriter) line(symbol, label, detail string) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		fmt.Fprintf(t.w, "%s %s\n", symbol, label)
		return
	}
	fmt.Fprintf(t.w, "%s %s: %s\n", symbol, label, detail)
}

func (t *textWriter) Step(label, detail string) { t.line("→", label, detail) }
func (t *textWriter) Ok(label, detail string)   { t.line("✓", label, detail) }
func (t *textWriter) Fail(label, detail string) { t.line("✗", label, detail) }
func (t *textWriter) Warn(label, detail string) { t.line("!", label, detail) }

func (t *textWriter) Summary(passed, failed int) {
	if failed == 0 {
		fmt.Fprintf(t.w, "\nSummary: %d/%d passed\n", passed, passed+failed)
		return
	}
	fmt.Fprintf(t.w, "\nSummary: %d passed, %d failed\n", passed, failed)
}
