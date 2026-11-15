package main

import (
	"context"
	"os"

	"github.com/boxt/boxt/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), cli.New(), os.Args[1:]))
}
