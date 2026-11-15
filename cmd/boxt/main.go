package main

import (
	"context"
	"os"

	"github.com/daveagill/boxt/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), cli.New(), os.Args[1:]))
}
