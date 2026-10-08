package scenario

import (
	"context"
	"fmt"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/graph"
)

// Scenario is one narrated tour: what it shows, the graph nodes it
// declares, and the ordered steps that show it. It states no preconditions
// of its own: the nodes are its dependencies, which its command declares
// with Use, so the dispatcher builds and starts exactly those before the
// first step, and a dependency that cannot be reached fails the run at
// start, naming its node.
type Scenario struct {
	Name    string      // the word after "blobfs scenario"
	Summary string      // the line the scenario parent's help and the listing print
	Nodes   []graph.Ref // the nodes the command declares, in order, which the listing names
	Steps   []Step
}

// Step is one beat of the narration: the sentence saying what is about to
// happen, and the action that does it. The action closes over the nodes it
// reads and reads their values with inv.Get, from the System the
// dispatcher built and started for the scenario's command, and reports
// what it observed through r.
type Step struct {
	Intent string
	Action func(ctx context.Context, inv *cli.Invocation, r *Reporter) error
}

// command builds the leaf command that runs s: named and summarized as s
// is, taking no arguments, and declaring s's nodes with Use. The
// dispatcher builds and starts the declared nodes before the command runs,
// so the first step runs over a System already up, and each step narrates
// through a Reporter over the command's stdout.
func command(s Scenario) *cli.Command {
	return (&cli.Command{
		Name:    s.Name,
		Summary: s.Summary,
		Args:    cli.NoArgs,
		Run: func(ctx context.Context, inv *cli.Invocation) error {
			return run(ctx, s, inv)
		},
	}).Use(s.Nodes...)
}

// run runs s's steps in order with inv, the scenario command's Invocation,
// narrating each intent through a Reporter over inv.Stdout before its
// action. It stops at the first action that returns an error, which it
// returns naming the step by its number and its intent, so a failed run's
// report says which beat failed; the dispatcher labels it with the
// command's path, which names the scenario. Every action works through
// ctx, so a ctx that ends fails the step under way, which names it.
func run(ctx context.Context, s Scenario, inv *cli.Invocation) error {
	r := &Reporter{w: inv.Stdout}
	for i, step := range s.Steps {
		r.heading(i+1, len(s.Steps), step.Intent)
		if err := step.Action(ctx, inv, r); err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.Intent, err)
		}
	}
	return nil
}
