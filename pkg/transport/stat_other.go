//go:build !linux

package transport

import "fmt"

func deviceOf(path string) (string, error) {
	return "", fmt.Errorf("discovering the disk of %s needs Linux", path)
}
