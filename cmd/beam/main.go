// Command beam connects the machines one person owns.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: beam version")
	os.Exit(2)
}
