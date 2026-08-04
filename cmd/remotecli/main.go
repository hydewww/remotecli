package main

import (
	"os"

	"remotecli/internal/remotecli"
)

func main() {
	os.Exit(remotecli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
