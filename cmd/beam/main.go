// Command beam connects the machines one person owns.
package main

import (
	"os"

	"github.com/notaharness/beam/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Main(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}
