// Package version holds the Strife version. Release builds set it with
//
//	go build -ldflags "-X github.com/kemalhafizh/strife/internal/version.Version=v1.0.0"
package version

// Version is the Strife version; "dev" builds come from source.
var Version = "1.0.0"
