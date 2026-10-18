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
	agent.RunHelper() // restarting a database inside its container
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
	token := fs.String("token", os.Getenv("STRIFE_AGENT_TOKEN"), "bearer token clients must present (default $STRIFE_AGENT_TOKEN)")
	insecure := fs.Bool("insecure-no-token", false, "allow serving a network address without a token (anyone who can reach it can run commands as this user)")
	cgroupRoot := fs.String("cgroup-root", "", "the node's cgroup v2 filesystem, for faults on other containers (default /sys/fs/cgroup; a DaemonSet mounts the node's, e.g. /host/sys/fs/cgroup)")
	requireTarget := fs.Bool("require-target", false, "act only on target containers, never on the node: refuse requests that name no container, network faults on containers using the node's network, and scripts (/exec) (for an agent sharing its node's namespaces, like a DaemonSet)")
	ioRoot := fs.String("io-root", "", "a directory on the disk holding container data, for io-stall with io_device \"auto\" (e.g. /host/var/lib/containerd)")

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

	isUnix := strings.HasPrefix(*listenAddr, "/") || strings.HasSuffix(*listenAddr, ".sock")
	if *token == "" && !isUnix && !loopback(*listenAddr) && !*insecure {
		return fmt.Errorf("refusing to serve %s without a token: the agent executes commands for its clients; set -token or $STRIFE_AGENT_TOKEN (or pass -insecure-no-token on an isolated network)", *listenAddr)
	}

	cfg := agent.ServerConfig{
		NodeName:      name,
		Interface:     *iface,
		Token:         *token,
		CgroupRoot:    *cgroupRoot,
		IORoot:        *ioRoot,
		RequireTarget: *requireTarget,
	}
	srv := agent.NewServer(cfg)

	var l net.Listener
	var err error

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

	// Capabilities also sets up the packet filter, so the first partition
	// does not pay for creating the agent's tables.
	caps := srv.Capabilities()
	filter := "none"
	switch {
	case *requireTarget:
		filter = "per-container (targets only)"
	case caps.NFTables:
		filter = "nftables"
	case caps.IPTables:
		filter = "iptables"
	}
	fmt.Printf("strife-agent starting on %s [%s] (netlink=%v filter=%s cgroup_v2=%v os=%s/%s)\n",
		*listenAddr, name, caps.Netlink, filter, caps.CgroupV2, caps.OS, caps.Arch)

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

// loopback reports whether addr only accepts connections from this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
