package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kaladinstorm84/SignalBox/internal/config"
	"github.com/kaladinstorm84/SignalBox/internal/engine"
)

const chunksUsage = `Usage:
  signalbox chunks list <execution>
  signalbox chunks add <execution> <title...>
  signalbox chunks done <execution> <chunk-id>
  signalbox chunks remove <execution> <chunk-id>

Flags:
  --addr host:port   SignalBox server (default: server in signalbox.yaml)
  --by name          Who made the change (default: cli)
`

var chunksHTTPClient = &http.Client{Timeout: 15 * time.Second}

// cmdChunks manages an execution's chunks through a running server, so
// changes reach live sessions immediately.
func cmdChunks(args []string, out io.Writer) error {
	var positional []string
	addr, by := "", "cli"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || (name != "addr" && name != "by") {
			positional = append(positional, arg)
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return fmt.Errorf("flag --%s needs a value\n\n%s", name, chunksUsage)
			}
			i++
			value = args[i]
		}
		if name == "addr" {
			addr = value
		} else {
			by = value
		}
	}
	if len(positional) < 2 {
		return fmt.Errorf("missing arguments\n\n%s", chunksUsage)
	}
	if addr == "" {
		var err error
		if addr, err = configuredServerAddr(); err != nil {
			return fmt.Errorf("%w (or pass --addr host:port)", err)
		}
	}
	base := "http://" + addr + "/api/executions/" + url.PathEscape(positional[1]) + "/chunks"

	switch sub := positional[0]; sub {
	case "list":
		if len(positional) != 2 {
			return fmt.Errorf("usage: signalbox chunks list <execution>")
		}
		var chunks []engine.Chunk
		if err := chunksRequest(http.MethodGet, base, nil, &chunks); err != nil {
			return err
		}
		if len(chunks) == 0 {
			fmt.Fprintln(out, "no chunks")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tTITLE\tCREATED BY\tDONE BY")
		for _, c := range chunks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Status, c.Title, c.CreatedBy, c.DoneBy)
		}
		return w.Flush()
	case "add":
		title := strings.Join(positional[2:], " ")
		if strings.TrimSpace(title) == "" {
			return fmt.Errorf("usage: signalbox chunks add <execution> <title...>")
		}
		var chunk engine.Chunk
		if err := chunksRequest(http.MethodPost, base, map[string]string{"title": title, "by": by}, &chunk); err != nil {
			return err
		}
		fmt.Fprintf(out, "added %s: %s\n", chunk.ID, chunk.Title)
	case "done":
		if len(positional) != 3 {
			return fmt.Errorf("usage: signalbox chunks done <execution> <chunk-id>")
		}
		if err := chunksRequest(http.MethodPost, base+"/"+url.PathEscape(positional[2])+"/done", map[string]string{"by": by}, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s marked done\n", strings.ToUpper(positional[2]))
	case "remove":
		if len(positional) != 3 {
			return fmt.Errorf("usage: signalbox chunks remove <execution> <chunk-id>")
		}
		if err := chunksRequest(http.MethodDelete, base+"/"+url.PathEscape(positional[2]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s removed\n", strings.ToUpper(positional[2]))
	default:
		return fmt.Errorf("unknown chunks command %q\n\n%s", sub, chunksUsage)
	}
	return nil
}

func configuredServerAddr() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return "", err
	}
	host := cfg.Server.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, fmt.Sprint(cfg.Server.Port)), nil
}

func chunksRequest(method, target string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := chunksHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("contacting SignalBox server (is `signalbox serve` running?): %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("%s", apiErr.Error)
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if result != nil {
		return json.Unmarshal(data, result)
	}
	return nil
}
