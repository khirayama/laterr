// Command laterr runs commands later.
package main

import (
	"os"

	"github.com/khirayama/laterr/internal/cli"
)

// version is overridden at release time with -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version))
}
