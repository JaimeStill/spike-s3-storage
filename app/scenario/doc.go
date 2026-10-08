// Package scenario holds blobfs's scenarios, the narrated tours that
// blobfs scenario runs, and the runner and reporter they narrate through.
// It is blobfs's own package, shaped like slab's scenario package on
// package cli in place of cobra, and without slab's needs: a scenario's
// dependencies are the graph nodes it declares, and nothing else. There is
// no registry: [Commands] builds the scenario parent over the composition
// root's nodes and returns it as a slice, the same way a domain package's
// Commands is called and mounted.
//
// Each tour works over the files domain's API: each step calls the
// Service or the Storage, as a direct command does, and shows the result
// as that command prints it. A step is a function that closes over the
// nodes it reads and reads their values with inv.Get. Each tour's command
// declares those nodes with Use, like any command, so the dispatcher
// builds and starts only those, under the lifecycle, before the first step
// runs; a dependency that cannot be reached fails the run at start,
// reported once and naming its node, before anything is narrated. The
// directories tour declares the files node alone, so it builds Postgres
// and never the object store; the files tour declares the files and
// storage nodes, so it builds both. Each works in its own top-level
// directory, clears it first when an earlier run left it behind, and
// removes it last, so it succeeds twice in a row.
//
// The scenario parent, run alone, prints its help, which ends with the
// listing of each scenario and the nodes it declares; the root's help ends
// with the same listing, which [WriteListing] writes for both.
//
// The package exports:
//
//   - [Scenario], an ordered list of [Step]s over the graph nodes it
//     declares
//   - [Reporter], which writes a step's narration
//   - [Commands], the package's command surface: the scenario parent with a
//     leaf per tour, as a slice the root mounts with
//     root.Add(scenario.Commands(...)...)
//   - [WriteListing], which writes the scenarios and the nodes each
//     declares, as a help footer prints them
package scenario
