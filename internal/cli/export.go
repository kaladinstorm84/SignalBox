package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/kaladinstorm84/SignalBox/internal/exporter"
)

const exportUsage = `Usage:
  signalbox export <pipeline> --output script.py [--format python] [--force]

Export a standalone Python 3.10+ runner; SignalBox is not needed to run it.
Sequential standard, llm_gate and approval nodes using CLI runtimes are supported.
Unsupported workflow features fail explicitly. Run the script with --help for
parameters, logging, working-directory and checkpoint/resume options.
`

func cmdExport(args []string, out io.Writer) error {
	a, err := parseOpsArgs(args, []string{"output", "format"}, []string{"force"})
	if err != nil {
		return err
	}
	if len(a.positional) != 1 || a.value("output") == "" {
		return fmt.Errorf("%s", exportUsage)
	}
	if format := a.value("format"); format != "" && format != "python" {
		return fmt.Errorf("unsupported export format %q (want python)", format)
	}
	cfg, pipelines, err := loadProject()
	if err != nil {
		return err
	}
	p, ok := pipelines[a.positional[0]]
	if !ok {
		return fmt.Errorf("unknown pipeline %q", a.positional[0])
	}
	data, err := exporter.Python(p, cfg)
	if err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if a.switches["force"] {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	path := a.value("output")
	file, err := os.OpenFile(path, flags, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	if err != nil {
		return fmt.Errorf("creating Python export: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("writing Python export: %w", err)
	}
	_, err = fmt.Fprintf(out, "exported %q to %s\n", p.Name, path)
	return err
}
