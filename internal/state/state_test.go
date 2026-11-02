package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xferrous/warren/internal/config"
)

func TestSaveLoadListDelete(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &Record{
		Name:   "work",
		Image:  "warren-nixos:latest",
		User:   "agent",
		Spaces: []string{"base"},
		Mounts: []config.Mount{{ID: "m1", Host: "/tmp", Guest: "/tmp", ReadOnly: true}},
		Env:    map[string]string{"A": "b"},
	}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	got, err := Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "work" || len(got.Mounts) != 1 || got.Env["A"] != "b" {
		t.Fatalf("round trip = %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}
	time.Sleep(time.Millisecond)

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "work" {
		t.Fatalf("list = %+v", list)
	}
	if err := Delete("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("work"); !os.IsNotExist(err) {
		t.Fatalf("expected not exist, got %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"work":           "work",
		"my project":     "my-project",
		"~/weird/name!!": "weird-name",
		"":               "warren",
		"...":            "warren",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	unlock, err := Lock("work")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "warren", "work.json.lock")); err != nil {
		t.Fatal(err)
	}
}
