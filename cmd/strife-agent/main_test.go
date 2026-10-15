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
