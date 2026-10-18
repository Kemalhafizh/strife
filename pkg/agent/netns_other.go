//go:build !linux

package agent

import "errors"

func inNetns(pid int, fn func() error) error {
	return errors.New("agent: acting on another container needs Linux network namespaces")
}
