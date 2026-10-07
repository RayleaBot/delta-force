package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RayleaBot/delta-force/internal/plugin"
)

func main() {
	if err := plugin.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
