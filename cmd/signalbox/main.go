// Command signalbox is a YAML based AI agent workflow system with human
// approval stages. See `signalbox help`.
package main

import (
	"os"

	"github.com/gitea/signalbox/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
