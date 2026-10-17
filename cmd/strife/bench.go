package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kemalhafizh/strife/pkg/bench"
	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/runner"
	"github.com/kemalhafizh/strife/pkg/transport"
)

const benchUsage = `Usage:
  strife bench inject [flags]   measure how precisely faults land on a real cluster

The cluster config must describe every transport compared: a container per
node for docker, an agent address (or agent_port) per node for agent.
`

func cmdBench(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] != "inject" {
		fmt.Fprint(stderr, benchUsage)
		return exitUsage, fmt.Errorf("unknown or missing benchmark (inject)")
	}
	fs := flag.NewFlagSet("bench inject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "strife.json", "cluster config (nodes, db.process, transport settings)")
	transports := fs.String("transports", "docker,agent", "comma separated transports to compare")
	kinds := fs.String("faults", "partition,pause", "comma separated fault kinds to measure")
	cycles := fs.Int("cycles", 50, "inject+heal pairs per fault kind and transport")
	period := fs.Duration("period", 250*time.Millisecond, "time between consecutive transitions")
	jsonOut := fs.String("json", "", "write the full result as JSON to this file")
	mdOut := fs.String("markdown", "", "write the result table as Markdown to this file")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage, err
	}
	cfg, err := runner.Load(*cfgPath)
	if err != nil {
		return exitUsage, err
	}
	ks, err := nemesis.ParseKinds(*kinds)
	if err != nil {
		return exitUsage, err
	}
	var trs []string
	for _, t := range strings.Split(*transports, ",") {
		if t = strings.TrimSpace(t); t != "" {
			trs = append(trs, t)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// "auto" cgroups and disks are discovered once, through docker.
	if dock, err := transport.New(transport.Config{Type: "docker", Sudo: cfg.Transport.Sudo}); err == nil {
		if cfg, err = runner.ResolveHost(ctx, cfg, dock); err != nil {
			return exitError, err
		}
	}
	fmt.Fprintf(stdout, "strife bench inject: %d nodes, transports %v, faults %v, %d cycles every %v\n", len(cfg.Nodes), trs, ks, *cycles, *period)
	res, err := bench.Inject(ctx, bench.InjectConfig{
		Cluster: cfg, Transports: trs, Kinds: ks, Cycles: *cycles, Period: *period,
		Logf: func(format string, a ...any) { fmt.Fprintf(stdout, "  "+format+"\n", a...) },
	})
	if err != nil {
		return exitError, err
	}
	md := res.Markdown()
	fmt.Fprintf(stdout, "\n%s\n", md)
	if *mdOut != "" {
		if err := os.WriteFile(*mdOut, []byte(md), 0o644); err != nil {
			return exitError, err
		}
	}
	if *jsonOut != "" {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return exitError, err
		}
		if err := os.WriteFile(*jsonOut, append(data, '\n'), 0o644); err != nil {
			return exitError, err
		}
	}
	for _, r := range res.Rows {
		if r.Errors > 0 {
			return exitError, fmt.Errorf("%d %s %s transitions via %s failed: %s", r.Errors, r.Kind, r.Edge, r.Transport, r.FirstError)
		}
	}
	return exitOK, nil
}
