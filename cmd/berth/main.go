package main

import (
	"os"

	"github.com/kodnificent/berth/internal/cli"
)

var version string

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.Version(version)))
}
