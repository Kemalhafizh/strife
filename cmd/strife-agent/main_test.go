package main

import (
	"strings"
	"testing"
)

func TestAgentHelp(t *testing.T) {
	err := run([]string{"-h"})
	if err == nil {
		t.Errorf("expected error or flag.ErrHelp for -h")
	}
}

func TestAgentInvalidFlag(t *testing.T) {
	err := run([]string{"--nonexistent-flag"})
	if err == nil {
		t.Errorf("expected error for invalid flag")
	}
	if !strings.Contains(err.Error(), "provided but not defined") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAgentRefusesOpenNetworkWithoutToken(t *testing.T) {
	t.Setenv("STRIFE_AGENT_TOKEN", "")
	err := run([]string{"-listen", "0.0.0.0:0"})
	if err == nil || !strings.Contains(err.Error(), "without a token") {
		t.Fatalf("an unauthenticated network listener must be refused, got %v", err)
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:9292": true, "localhost:9292": true, "[::1]:9292": true,
		"0.0.0.0:9292": false, ":9292": false, "10.0.0.1:9292": false, "garbage": false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
