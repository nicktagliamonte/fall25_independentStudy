// Purpose: CLI entrypoint for the node binary.

package main

import (
	"log"
	"os"

	n "github.com/nicktagliamonte/fall25_independentStudy/pkg/node"
)

func main() {
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}
