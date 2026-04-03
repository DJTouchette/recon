package main

import (
	"os"

	"github.com/djtouchette/recon/cmd/recon/cli"
)

func main() {
	if err := cli.NewRootCmd("v0.1.0").Execute(); err != nil {
		os.Exit(1)
	}
}
