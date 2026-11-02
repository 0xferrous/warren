package shares

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xferrous/warren/internal/config"
)

func TestBindfsArgs(t *testing.T) {
	rw := bindfsArgs(false)
	for _, want := range []string{"--multithreaded", "--no-allow-other", "attr_timeout=0,entry_timeout=0,negative_timeout=0"} {
		if !contains(rw, want) {
			t.Errorf("rw args %v missing %q", rw, want)
		}
	}
	if contains(rw, "-r") {
		t.Errorf("rw args must not be read-only: %v", rw)
	}
	ro := bindfsArgs(true)
	if !contains(ro, "-r") {
		t.Errorf("ro args missing -r: %v", ro)
	}
}

func TestStagingPaths(t *testing.T) {
	m := config.Mount{ID: "abc"}
	if got, want := StagingPath("/state/vm/shares", m), "/state/vm/shares/hot/abc"; got != want {
		t.Fatalf("staging = %q, want %q", got, want)
	}
	if got, want := GuestStagingPath(m), "/var/warren/shares/hot/abc"; got != want {
		t.Fatalf("guest staging = %q, want %q", got, want)
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.Contains(a, want) {
			return true
		}
	}
	return false
}

// TestBindfsStageLive is opt-in because it mounts a real FUSE filesystem.
// Run with a bindfs binary and a working fusermount3:
//
//	WARREN_BINDFS=/path/to/bindfs go test -run TestBindfsStageLive -v ./internal/shares
func TestBindfsStageLive(t *testing.T) {
	if os.Getenv("WARREN_TEST_BINDFS") == "" && os.Getenv("WARREN_BINDFS") == "" {
		t.Skip("set WARREN_BINDFS to run the live bindfs test")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse is not available")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dstRoot := filepath.Join(root, "shares")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "hello"), []byte("bindfs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := config.Mount{ID: "live", Host: src, Guest: "/mnt/live"}
	if err := stageHost(dstRoot, m); err != nil {
		t.Fatalf("stageHost: %v", err)
	}
	dst := StagingPath(dstRoot, m)
	if !isMountpoint(dst) {
		t.Fatalf("%s is not a mountpoint", dst)
	}
	b, err := os.ReadFile(filepath.Join(dst, "hello"))
	if err != nil || string(b) != "bindfs\n" {
		t.Fatalf("read through bindfs = %q, %v", b, err)
	}
	if err := unstageHost(dstRoot, m); err != nil {
		t.Fatalf("unstageHost: %v", err)
	}
	if isMountpoint(dst) {
		t.Fatalf("%s is still mounted", dst)
	}
}
