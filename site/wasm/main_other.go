//go:build !(js && wasm)

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "build this command with GOOS=js GOARCH=wasm; see site/README.md")
	os.Exit(2)
}
