package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/kaladinstorm84/SignalBox/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	target := fs.String("url", "", "running SignalBox server origin (default: server in signalbox.yaml)")
	allowReview := fs.Bool("allow-review", false, "expose delegated approval and manual-edit tools; actor is mcp-client")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: signalbox mcp [--url http://host:port] [--allow-review]")
	}
	if *target == "" {
		addr, err := configuredServerAddr()
		if err != nil {
			return fmt.Errorf("%w (or pass --url http://host:port)", err)
		}
		*target = "http://" + addr
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	server, err := mcpserver.New(ctx, mcpserver.Options{URL: *target, Version: Version, AllowReview: *allowReview})
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}
