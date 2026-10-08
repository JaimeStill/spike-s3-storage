package cli

import (
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
)

// help returns cmd's generated help: its summary, usage line, subcommands,
// own flags, and inherited flags, each section left out when it is empty,
// and last what cmd's own Footer writes, after a blank line.
func help(cmd *Command) string {
	var b strings.Builder
	if cmd.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", cmd.Summary)
	}
	fmt.Fprintf(&b, "Usage:\n  %s\n", usageLine(cmd))

	var commands []row
	for _, sub := range cmd.children {
		commands = append(commands, row{sub.Name, sub.Summary})
	}
	writeSection(&b, "Commands", commands)

	own := flagsOf(cmd, func(name string) bool { return !cmd.inherited[name] })
	own = append(own, row{"--help", "Show help for " + cmd.path()})
	writeSection(&b, "Flags", own)
	writeSection(&b, "Global flags", flagsOf(cmd, func(name string) bool { return cmd.inherited[name] }))

	if len(cmd.children) > 0 {
		fmt.Fprintf(&b, "\nRun '%s <command> --help' for help on a command.\n", cmd.path())
	}
	if cmd.Footer != nil {
		var footer strings.Builder
		cmd.Footer(&footer)
		if footer.Len() > 0 {
			fmt.Fprintf(&b, "\n%s", footer.String())
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// usageLine returns cmd's one-line usage, such as
// "blobfs <command> [flags]" for a parent or
// "blobfs blob get [flags] <path>" for a leaf.
func usageLine(cmd *Command) string {
	parts := []string{cmd.path()}
	if len(cmd.children) > 0 {
		parts = append(parts, "<command>")
	}
	parts = append(parts, "[flags]")
	if cmd.Synopsis != "" {
		parts = append(parts, cmd.Synopsis)
	}
	return strings.Join(parts, " ")
}

// row is one line of a help section: a command or flag name, and the text
// beside it.
type row struct {
	name string // "version", or "--bucket string"
	text string // "Print the version", or "bucket to read (default \"logs\")"
}

// flagsOf returns a row for each flag on cmd's flag set whose name keep
// accepts, in lexical order.
func flagsOf(cmd *Command, keep func(name string) bool) []row {
	var lines []row
	cmd.Flags().VisitAll(func(f *flag.Flag) {
		if keep(f.Name) {
			lines = append(lines, describe(f, cmd.isRequired(f.Name)))
		}
	})
	return lines
}

// describe renders f as a flags-section row: its long name, the value
// placeholder flag.UnquoteUsage derives, and the default when it is not
// the zero value. A repeatable flag from [StringsVar] takes "string" as its
// placeholder, since each occurrence takes one, and says it repeats; a flag
// the command requires says so.
func describe(f *flag.Flag, required bool) row {
	kind, usage := flag.UnquoteUsage(f)
	_, repeatable := f.Value.(*stringsValue)
	if repeatable {
		if kind == "value" {
			kind = "string"
		}
		usage += " (repeatable)"
	}
	if required {
		usage += " (required)"
	}
	name := "--" + f.Name
	if kind != "" {
		name += " " + kind
	}
	switch f.DefValue {
	case "", "false", "0", "[]":
	default:
		if kind == "string" && !repeatable {
			usage += fmt.Sprintf(" (default %q)", f.DefValue)
		} else {
			usage += fmt.Sprintf(" (default %s)", f.DefValue)
		}
	}
	return row{name: name, text: usage}
}

// writeSection writes a section headed title with rows in aligned columns,
// or nothing when rows is empty.
func writeSection(b *strings.Builder, title string, rows []row) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	// A tabwriter over a strings.Builder cannot fail to write.
	tw := tabwriter.NewWriter(b, 0, 0, 3, ' ', 0)
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", r.name, r.text)
	}
	_ = tw.Flush()
}
