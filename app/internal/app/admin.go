package app

import (
	"github.com/standards-lab/sqlate/migrate"

	"github.com/JaimeStill/spike-s3-storage/app/admin/schema"
	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// defineAdmin defines the administration nodes on g into n: the migrator
// the schema group uses, over the infrastructure's sql node. It constructs
// nothing.
func defineAdmin(g *graph.Graph, n *Nodes) {
	n.Migrator = g.Define("migrator", newMigrator(n))
}

// mountAdmin mounts the administration commands at root: the schema group
// over the migrator node.
func mountAdmin(root *cli.Command, n *Nodes) {
	root.Add(schema.Commands(n.Migrator)...)
}

// newMigrator constructs the schema migrator over the sql node, the
// database's pool in sqlate's Postgres dialect. It does no I/O: the pool
// first connects when the lifecycle starts the database, after the Build,
// and the migrator itself opens nothing.
func newMigrator(n *Nodes) func(*graph.Scope) (*migrate.Migrator, error) {
	return func(s *graph.Scope) (*migrate.Migrator, error) {
		return schema.NewMigrator(s.Use(n.SQL))
	}
}
