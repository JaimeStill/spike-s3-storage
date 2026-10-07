// Command blobfs is the spike's command-line program. Its main sequence is
// the signal context and one call into the composition root, internal/app.
package main

import (
	"os"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
	"github.com/standards-lab/go-core/process"
)

func main() {
	os.Exit(run())
}

// run runs blobfs and returns its exit code. It is separate from main so the
// deferred stop releases the signal registration before os.Exit, which skips
// deferred calls.
func run() int {
	ctx, stop := process.SignalContext()
	defer stop()
	return app.New(cli.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}).Run(ctx, os.Args[1:])
}
