// Command deconflict is the entry point: the CLI, the Claude Code
// hooks, and the shared registry server are all this one binary.
package main

import (
	"os"

	"github.com/cloudcons/deconflict-cli/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
