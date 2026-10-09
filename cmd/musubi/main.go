// Command musubi is the CLI for the Musubi knowledge base. Subcommands
// (ingest, process, search, graph, trends, replay, forget) arrive with the
// first milestone; see docs/architecture.md.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("musubi", version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: musubi version")
	os.Exit(2)
}
