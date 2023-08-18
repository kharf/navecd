package main

import (
	"fmt"
	"os"

	"github.com/kharf/declcd/build/internal/build"
)

func main() {
	args := os.Args[1:]
	testToRun := build.TestAllArg
	if len(args) > 0 {
		testToRun = args[0]
	}

	if err := build.RunWith(
		build.ControllerGen,
		build.Test(testToRun),
	); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
