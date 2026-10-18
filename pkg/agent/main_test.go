package agent

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	RunHelper() // RestartIn starts this binary inside the container
	os.Exit(m.Run())
}
