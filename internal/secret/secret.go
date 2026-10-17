// Package secret resolves credentials referenced from config files, so that
// strife.json (which is copied into every report and attestation) never has
// to contain a password or token in plain text.
package secret

import (
	"fmt"
	"os"
	"strings"
)

// Resolve returns the secret a reference names:
//
//	env:NAME    the environment variable NAME (an error if unset or empty)
//	file:PATH   the contents of PATH, without the trailing newline
//
// Anything else is returned unchanged as a literal.
func Resolve(ref string) (string, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := strings.TrimPrefix(ref, "env:")
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("secret: environment variable %s is not set", name)
		}
		return v, nil
	case strings.HasPrefix(ref, "file:"):
		path := strings.TrimPrefix(ref, "file:")
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("secret: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return ref, nil
}

// IsReference reports whether s points at a secret instead of containing it.
func IsReference(s string) bool {
	return strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "file:")
}

// Redact returns what may be shown of a configured secret: references are
// kept (they reveal no secret and tell the reader where it came from),
// literals are masked.
func Redact(s string) string {
	if s == "" || IsReference(s) {
		return s
	}
	return "[redacted]"
}
