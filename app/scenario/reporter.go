package scenario

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Reporter writes a scenario's narration: a heading per step, which the
// runner prints, and under it, from the step, prose saying what it does
// and why, with [Reporter.Note], and the output its operation produced,
// with [Reporter.Show]. It writes plain text, with no color, to one
// writer.
type Reporter struct {
	w       io.Writer
	atBlank bool // whether the line just written was blank
	started bool // whether anything has been written
}

const indent = "  "

// columns is the width Note wraps prose to, the indent included.
const columns = 80

// heading prints step i of n's intent sentence as a heading, set off from
// the step before it by a blank line.
func (r *Reporter) heading(i, n int, intent string) {
	r.blank()
	r.printf("[%d/%d] %s\n", i, n, intent)
}

// Note prints prose, wrapped at columns with every line indented: one call
// is one description, however many lines it takes.
func (r *Reporter) Note(format string, args ...any) {
	for _, line := range wrap(fmt.Sprintf(format, args...), columns-len(indent)) {
		if line == "" {
			r.blank()
			continue
		}
		r.printf("%s%s\n", indent, line)
	}
}

// Show prints what render writes as a block indented one level past the
// prose, so a step's result reads apart from its narration: render is the
// same output a direct command writes, such as an output.Record. Nothing
// is printed when render fails, and its error is returned.
func (r *Reporter) Show(render func(io.Writer) error) error {
	var buf bytes.Buffer
	if err := render(&buf); err != nil {
		return err
	}
	text := strings.TrimRight(buf.String(), "\n")
	if text == "" {
		return nil
	}
	for line := range strings.SplitSeq(text, "\n") {
		r.printf("%s%s%s\n", indent, indent, line)
	}
	return nil
}

// showf shows one line formatted as fmt.Printf formats it, as Show
// shows what a render writes: a step's one-line result, written as the
// command it stands for prints it.
func (r *Reporter) showf(format string, args ...any) error {
	return r.Show(func(w io.Writer) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	})
}

// printf is the one write every channel goes through; a reporter has no
// way to act on a failed write, so the result is discarded here. Only
// blank's own call ever passes the bare "\n" format, so that is what
// atBlank tracks.
func (r *Reporter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, format, args...)
	r.atBlank = format == "\n"
	r.started = true
}

// blank prints one blank line, unless the reporter already sits on one or
// has written nothing yet, so the narration neither opens with a blank
// line nor doubles one.
func (r *Reporter) blank() {
	if r.atBlank || !r.started {
		return
	}
	r.printf("\n")
}

// wrap breaks text into lines of at most width columns, breaking only at
// whitespace: a word longer than width stands alone on a line that exceeds
// it rather than being split. A newline in text is a hard break, so a
// paragraph wraps on its own and an empty line between two stays empty.
func wrap(text string, width int) []string {
	var lines []string
	for para := range strings.SplitSeq(text, "\n") {
		lines = append(lines, wrapParagraph(para, width)...)
	}
	return lines
}

// wrapParagraph is wrap for one paragraph, which holds no newline: an
// empty or blank paragraph is one empty line.
func wrapParagraph(para string, width int) []string {
	words := strings.Fields(para)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}
