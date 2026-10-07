package schema

import (
	"context"
	"fmt"

	"github.com/standards-lab/sqlate/migrate"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// Commands builds the package's command surface over migrator, the
// composition root's node for the migrator [NewMigrator] builds: the
// schema command with its status, up, down, and reset subcommands, its one
// element, returned as a slice as every package's Commands is, so the root
// mounts each the same way. The group declares migrator with Use, and every
// subcommand inherits it, so the dispatcher builds and starts the
// migrator's dependencies before a verb's body runs and shuts them down
// after; each subcommand is a function of migrator, and its body reads the
// migrator with the Invocation's Get. Every subcommand takes no arguments.
func Commands(migrator *graph.Node[*migrate.Migrator]) []*cli.Command {
	return []*cli.Command{(&cli.Command{
		Name:    "schema",
		Summary: "Report, apply, revert, and reset the two migration sets",
	}).Use(migrator).Add(
		status(migrator),
		up(migrator),
		down(migrator),
		reset(migrator),
	)}
}

// status is the status subcommand: one row per set, bottom first, with the
// set's history table, its head, its latest version, the pending
// migrations by number and name, and whether the head is dirty.
func status(migrator *graph.Node[*migrate.Migrator]) *cli.Command {
	return &cli.Command{
		Name:    "status",
		Summary: "Show each set's head, latest version, pending migrations, and dirty mark",
		Args:    cli.NoArgs,
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			sets, err := inv.Get(migrator).Status(ctx)
			if err != nil {
				return err
			}
			return writeStatus(inv.Stdout, sets)
		},
	}
}

// up is the up subcommand: every pending migration applied, blobfs's set
// first.
func up(migrator *graph.Node[*migrate.Migrator]) *cli.Command {
	return &cli.Command{
		Name:    "up",
		Summary: "Apply every pending migration, blobfs's set first and then the app's",
		Args:    cli.NoArgs,
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			if err := inv.Get(migrator).Up(ctx); err != nil {
				return err
			}
			_, err := fmt.Fprintln(inv.Stdout, "schema up: both sets at head")
			return err
		},
	}
}

// down is the down subcommand: every applied migration reverted by
// revert, the app's set first, with the history tables kept.
func down(migrator *graph.Node[*migrate.Migrator]) *cli.Command {
	return &cli.Command{
		Name:    "down",
		Summary: "Revert every applied migration, the app's set first and then blobfs's; the history tables stay",
		Args:    cli.NoArgs,
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			if err := revert(ctx, inv.Get(migrator)); err != nil {
				return err
			}
			_, err := fmt.Fprintln(inv.Stdout, "schema down: both sets reverted")
			return err
		},
	}
}

// reset is the reset subcommand. It is destructive, so --yes is a required
// flag: the dispatcher refuses a reset without it as a usage error, before
// anything is built. A --yes given as false satisfies the requirement, so
// Validate refuses it too: it runs after the required-flag check and
// before the Build, so an unconfirmed reset never starts the database.
func reset(migrator *graph.Node[*migrate.Migrator]) *cli.Command {
	var yes bool
	cmd := &cli.Command{
		Name:    "reset",
		Summary: "Revert every set, the app's first, and drop the history tables; requires --yes",
		Args:    cli.NoArgs,
		Validate: func(inv *cli.Invocation) error {
			// The required-flag check has already refused an absent --yes.
			if inv.Changed("yes") && !yes {
				return cli.Usagef("--yes=false does not confirm the reset")
			}
			return nil
		},
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			if err := inv.Get(migrator).Reset(ctx); err != nil {
				return err
			}
			_, err := fmt.Fprintln(inv.Stdout, "schema reset: both sets reverted and their history tables dropped")
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the reset: every set is reverted and the history tables are dropped")
	cmd.Require("yes")
	return cmd
}
