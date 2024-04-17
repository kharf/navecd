package main

import (
	"fmt"
	"os"

	"github.com/kharf/declcd/build/internal/build"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Println("Version arg required")
		os.Exit(1)
	}
	version := args[0]
	if err := build.RunWith(
		build.TestAll,
		build.Build(version),
		build.Publish(version),
	); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
