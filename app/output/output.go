package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Table writes header and then each row to w as aligned columns, cells
// separated by two spaces at least. A table with no rows still writes its
// header, so an empty result is visible as one.
func Table(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(header, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// Field is one line of a record: a label and its value.
type Field struct {
	Name  string
	Value string
}

// Record writes one record to w as aligned label and value lines, one per
// field in the order given, each label followed by a colon. It is how a
// command shows one row, where a listing shows many.
func Record(w io.Writer, fields []Field) error {
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
	for _, f := range fields {
		if _, err := fmt.Fprintf(tw, "%s:\t%s\n", f.Name, f.Value); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// Count renders n with the noun that agrees with it: "1 file" with
// singular, and "0 files" or "2 files" with plural, so no count a command
// prints reads "1 files".
func Count[N int | int64](n N, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}
