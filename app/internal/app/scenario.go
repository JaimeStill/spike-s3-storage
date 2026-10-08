package app

import (
	"io"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/scenario"
)

// mountScenario mounts the scenarios' commands at root over the files and
// storage nodes: the scenario parent, in which each scenario's leaf
// declares the nodes its tour reads, so the dispatcher builds only those
// when it runs.
func mountScenario(root *cli.Command, n *Nodes) {
	root.Add(scenario.Commands(n.Files, n.Storage)...)
}

// scenarioListing returns the writer of the scenario listing the scenario
// parent's help ends with, which the root's help footer ends with too, as
// spike-blobfs's root help appended it. It declares no node, so the help
// builds nothing.
func scenarioListing(n *Nodes) func(w io.Writer) {
	return func(w io.Writer) { scenario.WriteListing(w, n.Files, n.Storage) }
}
