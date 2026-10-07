package scenario

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// scenarios returns blobfs's scenarios over the composition root's files
// and storage nodes, in presentation order: the directories tour, which
// declares the files node alone, then the files tour, which declares both.
func scenarios(svc *graph.Node[*files.Service], st *graph.Node[*files.Storage]) []Scenario {
	return []Scenario{
		directoriesTour(svc),
		filesTour(svc, st),
	}
}

// Commands builds the package's command surface over the composition
// root's files and storage nodes: the scenario parent, its one element,
// returned as a slice as every package's Commands is, so the root mounts
// each the same way. The parent has one leaf per scenario, each declaring
// its own nodes with Use; the parent declares none, so a tour brings up
// only what it declares. Run alone, the parent prints its help, which ends
// with the listing [WriteListing] writes.
func Commands(svc *graph.Node[*files.Service], st *graph.Node[*files.Storage]) []*cli.Command {
	parent := &cli.Command{
		Name:    "scenario",
		Summary: "Run a narrated scenario over the commands",
		Footer:  func(w io.Writer) { WriteListing(w, svc, st) },
	}
	for _, s := range scenarios(svc, st) {
		parent.Add(command(s))
	}
	return []*cli.Command{parent}
}

// WriteListing writes the scenario listing over the files and storage
// nodes to w, as a help Footer prints it: a Scenarios heading, then one
// line per scenario, its name and summary, followed by a uses line naming
// the nodes the scenario declares, in the order it declares them. It is
// the one source of the listing, which the scenario parent's help and the
// root's help both end with. It prints what a scenario declares, not the
// nodes those reach: the graph discovers a node's dependencies only by
// running its constructor, and the listing builds nothing.
//
// The columns are laid out as package cli lays out a help section, by a
// tabwriter with a padding of 3, so in the scenario parent's help the
// listing's names line up with the Commands section above it, where the
// same names appear.
func WriteListing(w io.Writer, svc *graph.Node[*files.Service], st *graph.Node[*files.Storage]) {
	_, _ = io.WriteString(w, "Scenarios:\n")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, s := range scenarios(svc, st) {
		names := make([]string, len(s.Nodes))
		for i, n := range s.Nodes {
			names[i] = n.Name()
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Summary)
		_, _ = fmt.Fprintf(tw, "  \tuses %s\n", strings.Join(names, ", "))
	}
	_ = tw.Flush()
}
