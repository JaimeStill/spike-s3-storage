package app

import (
	bfdata "github.com/standards-lab/blobfs/data"
	blobfspg "github.com/standards-lab/blobfs/postgres"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// defineDomain defines the domain nodes on g into n: the files Service the
// directory and bookmark commands use, over the infrastructure's sql node
// and never its object store, and the files domain's Storage the object
// commands use, over the Service and the object store. It constructs
// nothing.
func defineDomain(g *graph.Graph, n *Nodes) {
	n.Files = g.Define("files", newFiles(n))
	n.Storage = g.Define("storage", newStorage(n))
}

// mountDomain mounts the files domain's commands at root, each declaring
// the node it reads.
func mountDomain(root *cli.Command, n *Nodes) {
	root.Add(files.Commands(n.Files, n.Storage)...)
}

// newFiles constructs the files Service over the sql node, the database's
// pool in sqlate's Postgres dialect, with blobfs's Postgres engine: this is
// the one place the engine is named, as it is fixed for the program. It
// does no I/O. The Service's own Start, its statement check, is the node's
// start, which runs once the database has started, so a schema that is not
// applied fails the command at start, labelled with the node's name, before
// its body runs. The Service holds nothing to shut down.
func newFiles(n *Nodes) func(*graph.Scope) (*files.Service, error) {
	return func(s *graph.Scope) (*files.Service, error) {
		return files.New(s.Use(n.SQL), bfdata.WithEngine(blobfspg.Engine))
	}
}

// newStorage constructs the object operations over the files Service and
// the object store. It does no I/O and records no start of its own: the
// store's start creates its container and probes it, and the Service's
// start checks the statements, so by the time the command's body runs
// both are known to work, and a store that cannot be reached fails the
// command at start, labelled with the store's node name.
func newStorage(n *Nodes) func(*graph.Scope) (*files.Storage, error) {
	return func(s *graph.Scope) (*files.Storage, error) {
		return files.NewStorage(s.Use(n.Files), s.Use(n.Store)), nil
	}
}
