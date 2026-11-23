// Command golab is an unofficial Go implementation of the Google Colab CLI.
package main

import (
	"context"
	"os"

	"github.com/20Youssef10/golab/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:]))
}
