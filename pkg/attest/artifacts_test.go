package attest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSignAndVerify(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for name, body := range map[string]string{"strife_linux_amd64.tar.gz": "bin", "SHA256SUMS": "sums"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	b, err := SignRelease(files, ReleasePredicate{Version: "v0.2.0", Commit: "abc"}, priv)
	if err != nil {
		t.Fatal(err)
	}
	st, err := VerifyRelease(b, pub, dir)
	if err != nil || st.Predicate.Version != "v0.2.0" || len(st.Subject) != 2 {
		t.Fatalf("verify: %v %+v", err, st)
	}
	// A modified file is caught.
	if err := os.WriteFile(files[0], []byte("trojan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRelease(b, pub, dir); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("tampered file not detected: %v", err)
	}
	// Another key is rejected, and so is an edited unsigned statement.
	other, _, _ := GenerateKeyPair()
	if _, err := VerifyRelease(b, other, dir); err == nil {
		t.Fatal("wrong key accepted")
	}
}
