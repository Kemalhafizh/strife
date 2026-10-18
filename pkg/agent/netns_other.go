//go:build !linux

package agent

import (
	"errors"
	"os"
)

var errNoNetns = errors.New("agent: acting on another container needs Linux network namespaces")

func openNetns(pid int) (*os.File, string, error) { return nil, "", errNoNetns }

func inNetns(ns *os.File, fn func() error) error { return errNoNetns }
