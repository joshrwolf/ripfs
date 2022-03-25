package main

import (
	"context"
	"os"

	"github.com/joshrwolf/ripfs/cmd/ripfs/cli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cli.New().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
