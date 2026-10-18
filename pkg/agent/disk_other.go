//go:build !linux

package agent

import "errors"

func diskOf(sys, path string) (string, error) {
	return "", errors.New("agent: finding the disk under a path needs Linux")
}
