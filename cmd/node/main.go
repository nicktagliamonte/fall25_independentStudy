// Purpose: Thin CLI entrypoint for node; delegates to package node.

package main

import (
	"log"

	"github.com/nicktagliamonte/fall25_independentStudy/pkg/node"
)

func main() {
	if err := node.Run(); err != nil {
		log.Fatal(err)
	}
}
