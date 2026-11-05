// Command signalbox is a YAML based AI agent workflow system with human
// approval stages. See `signalbox help`.
package main

import (
	"os"

	"github.com/kaladinstorm84/SignalBox/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
