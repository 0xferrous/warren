package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := writeConfig(t, "")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Global.Image != DefaultImage || c.Global.Memory != DefaultMemory {
		t.Fatalf("unexpected defaults: %+v", c.Global)
	}
	if c.Global.GuestHome != DefaultGuestHome || c.Global.GuestUser != DefaultGuestUser {
		t.Fatalf("unexpected guest defaults: %+v", c.Global)
	}
}

func TestResolveExtendsAndDedup(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := writeConfig(t, `
[global]
default_spaces = ["base"]
guest_home = "/home/agent"

[spaces.base]
rw_mounts = ["`+a+`:~/workspace/a"]

[spaces.child]
extends = ["base"]
ro_mounts = ["`+a+`:~/ro-a", "`+b+`:~/ro-b"]
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve([]string{"child"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(r.Mounts), 3; got != want {
		t.Fatalf("mounts = %d, want %d: %+v", got, want, r.Mounts)
	}
	if r.Mounts[0].Guest != "/home/agent/workspace/a" {
		t.Fatalf("guest home not expanded: %+v", r.Mounts[0])
	}
	if r.Mounts[0].ReadOnly {
		t.Fatal("rw mount marked read-only")
	}
	if !r.Mounts[1].ReadOnly {
		t.Fatal("ro mount not marked read-only")
	}
	if len(r.Space) != 2 || r.Space[0] != "base" || r.Space[1] != "child" {
		t.Fatalf("space order = %v", r.Space)
	}
}

func TestResolveMissingPathIsWarning(t *testing.T) {
	path := writeConfig(t, `
[spaces.base]
rw_mounts = ["/nope/does/not/exist:~/x"]
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve([]string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Mounts) != 0 || len(r.Warnings) != 1 {
		t.Fatalf("mounts=%v warnings=%v", r.Mounts, r.Warnings)
	}
}

func TestResolveCycle(t *testing.T) {
	path := writeConfig(t, `
[spaces.a]
extends = ["b"]
[spaces.b]
extends = ["a"]
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve([]string{"a"}); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolveUnknownSpace(t *testing.T) {
	path := writeConfig(t, "")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve([]string{"missing"}); err == nil {
		t.Fatal("expected unknown space error")
	}
}

func TestResolveFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(file, []byte("[user]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, `
[spaces.base]
files = ["`+file+`:~/.gitconfig"]
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve([]string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 1 {
		t.Fatalf("files = %+v", r.Files)
	}
	if r.Files[0].Guest != "/home/agent/.gitconfig" {
		t.Fatalf("guest = %s", r.Files[0].Guest)
	}
	if r.Files[0].Mode != 0o600 {
		t.Fatalf("mode = %o", r.Files[0].Mode)
	}
	if string(r.Files[0].Content) != "[user]\n" {
		t.Fatalf("content = %q", r.Files[0].Content)
	}
}

func TestResolveEnvPrecedence(t *testing.T) {
	path := writeConfig(t, `
[global]
env = { A = "global", B = "global" }

[spaces.base]
env = { B = "space", C = "space" }
`)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Resolve([]string{"base"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Env["A"] != "global" || r.Env["B"] != "space" || r.Env["C"] != "space" {
		t.Fatalf("env = %v", r.Env)
	}
}
