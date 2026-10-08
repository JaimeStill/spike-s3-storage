// Package integration holds the black-box suite over the built blobfs
// binary, run by `mise run integration` against its isolated compose
// project. Its tests, behind the integration build tag, build cmd/blobfs
// once, run it as a child process configured only through its environment,
// arguments, and standard input, and check its output, its exit code, and
// its effect on a throwaway database and a container of each test's own.
// Each run is bounded by go-core's processtest.Failsafe, and so is every
// wait on a condition, which polls it through processtest.WaitFor.
//
// The suite reaches every state through the surfaces production has: the
// CLI for state, the environment for configuration, the network for
// faults, and signals and the exit code for lifecycle. It writes nothing
// to the database or the store behind the binary's back, and reads them
// only through the binary, with one exception: the large-body tests read
// the store's bucket through an S3 client of their own, for what blobfs
// cannot show, the multipart uploads open in it, their parts, the objects
// in it, and an object's ETag.
//
//   - A pending row is a crashed put's: put - runs with its standard input
//     held open on a pipe, so it commits the pending row and waits on the
//     body; once stat in another run shows the row pending, the put is
//     killed with SIGKILL, which it cannot catch.
//   - An interrupted rm --recursive is a store outage mid-sweep, injected
//     at an exact request rather than a moment: the store is reached
//     through an HTTP relay over httputil.ReverseProxy, its retries off
//     through BLOBFS_STORAGE_OPTIONS_MAX_RETRIES, since the provider
//     retries the relay's 503. Armed, the relay lets a set number of blob
//     deletes through and answers every later one 503. blobfs's sweep is
//     sequential, walks a directory's files by name before its child
//     directories, and deletes a file's object before its row, so the
//     counts are exact: of a branch of three files and an empty directory,
//     the run with one delete allowed removes one file and the directory,
//     is refused the other two, and exits 1 reporting the refusal and
//     those counts; the branch stays deleting. The relay is disarmed
//     before the rerun, which removes the two files left and the branch.
//   - An unreachable store is an endpoint on a port nothing listens on.
//
// TestScript is one ordered script over the directory, object, and
// bookmark commands. TestTheStoreUnreachable closes the object store's
// endpoint: every directory and bookmark command still succeeds, and two
// object commands fail naming the store. TestAnInterruptedPut sends SIGINT
// to a put blocked on its held-open standard input, through main's signal
// context: it exits one, reporting the cancellation once.
// largebodies_test.go runs the binary at 5 MiB parts, through
// BLOBFS_STORAGE_OPTIONS_PART_SIZE, so a body of three parts takes the
// provider's multipart path. TestALargePut puts one from a local file and
// one from standard input: each round-trips through cat, is stored with an
// ETag in the multipart form, and leaves no upload open.
// TestAnInterruptedLargePut and TestACrashedLargePut stop a put - once the
// store shows its upload open with a part received, the put waiting on
// standard input for the next: SIGINT leaves no row, no object, and no
// open upload; SIGKILL leaves the pending row and the open upload, and a
// later put resumes the row under its key while the crashed put's upload
// stays open, an orphan only a lifecycle rule or a sweep frees.
// scenarios_test.go prints the scenario parent's help, with its listing,
// with nothing reachable, runs each tour twice in a row and after an
// interrupted run, and runs scenario directories with the store
// unreachable, where scenario files fails naming the store. Every run logs
// its command line and output, so go test -v prints the transcript.
//
// The package has no code outside its tests.
package integration
