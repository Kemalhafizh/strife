package secret

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Setenv("STRIFE_TEST_SECRET", "hunter2")
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"env:STRIFE_TEST_SECRET": "hunter2",
		"file:" + path:           "s3cret",
		"plain":                  "plain",
		"":                       "",
	} {
		if got, err := Resolve(in); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"env:STRIFE_TEST_UNSET_SECRET", "file:/nonexistent/strife"} {
		if _, err := Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) should fail", bad)
		}
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{"": "", "env:PW": "env:PW", "file:/run/pw": "file:/run/pw", "hunter2": "[redacted]"} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
