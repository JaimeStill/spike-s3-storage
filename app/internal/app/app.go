package app

import (
	"context"
	"io"

	"github.com/standards-lab/go-database"
	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
	"github.com/JaimeStill/spike-s3-storage/app/lifecycle"
)

// App is the blobfs program: its dependency graph and the [Nodes] that
// describe it, its command tree, and the streams it reads from and reports
// to.
type App struct {
	graph   *graph.Graph
	nodes   Nodes
	root    *cli.Command
	streams cli.Streams
}

// New describes the graph, builds the command tree, and returns the App. It
// is cold: it constructs nothing, reads no configuration, and writes
// nothing until [App.Run]. streams are what every run reads from and
// writes to: Stdin is what a command reads as standard input, such as
// put's content from -, and nothing reads it but such a command.
func New(streams cli.Streams) *App {
	a := &App{
		graph: graph.New(),
		root: &cli.Command{
			Name:    "blobfs",
			Summary: "blobfs manages files in a blob store.",
		},
		streams: streams,
	}
	defineInfrastructure(a.graph, &a.nodes)
	defineAdmin(a.graph, &a.nodes)
	defineDomain(a.graph, &a.nodes)
	a.root.Add(versionCommand())
	mountAdmin(a.root, &a.nodes)
	mountDomain(a.root, &a.nodes)
	mountScenario(a.root, &a.nodes)
	listing := scenarioListing(&a.nodes)
	a.root.Footer = func(w io.Writer) {
		_, _ = io.WriteString(w, idOperand+"\n")
		listing(w)
	}
	return a
}

// idOperand is the line the root's help footer opens with: the operand
// scheme every command that names an existing entry shares, so the
// commands' synopses can write <path|id:<uuid>> without each restating it.
const idOperand = "A path argument may instead be id:<uuid>, naming the directory or file by its id.\n"

// Run dispatches args, the program arguments without the program name, and
// returns the process exit code. The dispatcher builds the nodes the
// selected command's path declares with Use from the App's graph, with the
// lifecycle configuration node, and shuts what it built down before Run
// returns, so a Build, start, or shutdown error is reported with the
// command's result. An App runs one command at a time, since a graph.Graph
// is not safe for concurrent use.
func (a *App) Run(ctx context.Context, args []string) int {
	return cli.Run(ctx, a.root, args, a.streams, cli.WithGraph(a.graph, a.nodes.LifecycleConfig))
}

// Nodes is the App's graph, one handle per node: the single description of
// what blobfs is composed of. Each layer file's define function fills its
// own part of one Nodes value, and its constructors read the lower layers'
// nodes from that same value: the configurations and the infrastructure
// built from them, the migrator, and the files domain's two nodes. Each
// field's node is named as the dispatcher labels its errors.
type Nodes struct {
	DatabaseConfig  *graph.Node[database.Config]   // "database config"
	StorageConfig   *graph.Node[storage.Config]    // "storage config"
	LifecycleConfig *graph.Node[lifecycle.Config]  // "lifecycle config"
	Database        *graph.Node[*database.DB]      // "database"
	SQL             *graph.Node[*sqlate.DB]        // "sql"
	Store           *graph.Node[*storage.Store]    // "store"
	Migrator        *graph.Node[*migrate.Migrator] // "migrator"
	Files           *graph.Node[*files.Service]    // "files"
	Storage         *graph.Node[*files.Storage]    // "storage"
}

// Graph returns the graph a's commands are built from, as [New] described
// it. The program itself never calls it; it is published so a caller
// can, before the App runs, observe what a run builds with
// graph.Graph.Observe, or Replace a node's constructor with a substitute.
// A Replace changes the App itself, and the graph panics on a Replace once
// a run has built from it.
func (a *App) Graph() *graph.Graph { return a.graph }

// Nodes returns a handle on each of a's graph nodes, for a caller's Replace
// or for a command it adds over them.
func (a *App) Nodes() Nodes { return a.nodes }

// Root returns a's root command. A command a caller Adds to it before the
// App runs is part of the App's tree from then on.
func (a *App) Root() *cli.Command { return a.root }
