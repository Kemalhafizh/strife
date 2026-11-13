package transport

import "testing"

func TestFormatDev(t *testing.T) {
	cases := []struct {
		raw  uint64
		want string
	}{
		{0x0801, "8:1"},
		{0xfc10, "252:16"},
		{0x10301, "259:1"},
		{0x100800, "8:256"},
	}
	for _, tc := range cases {
		if got := formatDev(tc.raw); got != tc.want {
			t.Errorf("formatDev(0x%x) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestStatDeviceHelpers(t *testing.T) {
	dir := t.TempDir()
	dev, err := deviceOf(dir)
	if err != nil || dev == "" {
		t.Fatalf("deviceOf(%q) = %q, %v", dir, dev, err)
	}
	if _, err := blockDeviceOf(dir); err == nil {
		t.Fatalf("blockDeviceOf(%q) on directory must return an error", dir)
	}
}
