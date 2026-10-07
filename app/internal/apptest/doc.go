// Package apptest holds the fixtures internal/app's tests share for running
// blobfs with some of its graph's nodes Replace-d, over the composition
// [app.App] publishes: its graph and its nodes. A test builds its App with
// [app.New] and applies the fixtures to it before it runs, each a Replace
// or an observer on the App's graph.
//
// [Builds] records the name of every node a run's Build begins, through
// graph.Graph.Observe, so a test asserts which nodes a command builds from
// what the graph reports, with every constructor it does not swap left the
// production one. [Halt] fails the lifecycle configuration node, which the
// dispatcher adds to every Build as its last root, so a run's Build
// constructs everything the command declares and stops before anything
// starts. [ScriptDatabase] swaps the database for a pool over sqlate's
// scripted driver, and [FakeStore] the object store for go-storage's Store
// over a [storagetest.Fake], which may be down; each constructs its
// substitute and wraps no production constructor.
//
// The package exports:
//
//   - [Recorder], the events a test records, in order, and its
//     [Recorder.Record] and [Recorder.Log]
//   - [Builds], which returns a Recorder of the nodes the App's runs build
//   - [Halt], which stops the App's runs before anything starts, and
//     [ErrHalted], what such a run fails with
//   - [ScriptDatabase], which builds the database node over a scripted pool
//   - [FakeStore], which builds the store node over a Fake
package apptest
