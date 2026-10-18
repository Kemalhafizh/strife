//go:build !linux

package agent

import "errors"

// RestartIn is only supported on Linux.
func (p *ProcessEngine) RestartIn(startCmd string, c *container) error {
	return errors.New("process: restarting inside a container needs Linux")
}

// RunHelper does nothing outside Linux: RestartIn never starts a helper.
func RunHelper() {}
