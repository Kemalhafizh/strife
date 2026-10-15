// Command strife-agent is a lightweight, low-jitter per-node daemon that
// receives fault injection commands over a TCP or Unix socket and applies them
// directly to the Linux kernel via RTNETLINK, direct signals, and cgroup v2.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kemalhafizh/strife/pkg/agent"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "strife-agent error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("strife-agent", flag.ContinueOnError)
	listenAddr := fs.String("listen", fmt.Sprintf(":%d", agent.DefaultPort), "listen address (e.g. :9292, 0.0.0.0:9292, or /var/run/strife-agent.sock)")
	nodeName := fs.String("node", "", "node name (defaults to hostname)")
	iface := fs.String("iface", "eth0", "network interface for traffic control / netem")
	token := fs.String("token", "", "optional authorization bearer token")

	if err := fs.Parse(args); err != nil {
		return err
	}

	name := *nodeName
	if name == "" {
		h, err := os.Hostname()
		if err == nil {
			name = h
		} else {
			name = "node-1"
		}
	}

	cfg := agent.ServerConfig{
		NodeName:  name,
		Interface: *iface,
		Token:     *token,
	}
	srv := agent.NewServer(cfg)

	var l net.Listener
	var err error
	isUnix := strings.HasPrefix(*listenAddr, "/") || strings.HasSuffix(*listenAddr, ".sock")

	if isUnix {
		_ = os.Remove(*listenAddr)
		l, err = net.Listen("unix", *listenAddr)
		if err != nil {
			return fmt.Errorf("listen unix %s: %w", *listenAddr, err)
		}
		defer func() { _ = os.Remove(*listenAddr) }()
	} else {
		l, err = net.Listen("tcp", *listenAddr)
		if err != nil {
			return fmt.Errorf("listen tcp %s: %w", *listenAddr, err)
		}
	}
	defer l.Close()

	caps := srv.Capabilities()
	fmt.Printf("strife-agent starting on %s [%s] (netlink=%v cgroup_v2=%v os=%s/%s)\n",
		*listenAddr, name, caps.Netlink, caps.CgroupV2, caps.OS, caps.Arch)

	// Graceful shutdown on SIGINT / SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(l)
	}()

	select {
	case sig := <-sigCh:
		fmt.Printf("\nreceived signal %v, shutting down and healing faults...\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-serverErr:
		if err != nil && err != net.ErrClosed {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}
