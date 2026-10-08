package app

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
)

// versionCommand returns the dependency-free version command, which prints
// the main module's version.
func versionCommand() *cli.Command {
	return &cli.Command{
		Name:    "version",
		Summary: "Print the blobfs version",
		Args:    cli.NoArgs,
		Run: func(_ context.Context, inv *cli.Invocation) error {
			_, err := fmt.Fprintln(inv.Stdout, moduleVersion())
			return err
		},
	}
}

// moduleVersion returns the main module's version from the build info, or
// "(devel)" when the binary carries none, as under go test; go run reports
// "(devel)" itself.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "(devel)"
	}
	return info.Main.Version
}
