package cli

import (
	"flag"
	"testing"
)

func TestSplitFlake(t *testing.T) {
	cases := []struct {
		in, path, host string
		wantErr        bool
	}{
		{in: "../my-nix#agent", path: "../my-nix", host: "agent"},
		{in: "/a/b#c", path: "/a/b", host: "c"},
		{in: "no-hash", wantErr: true},
		{in: "#agent", wantErr: true},
		{in: "path#", wantErr: true},
	}
	for _, c := range cases {
		path, host, err := splitFlake(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitFlake(%q) expected error", c.in)
			}
			continue
		}
		if err != nil || path != c.path || host != c.host {
			t.Errorf("splitFlake(%q) = %q, %q, %v", c.in, path, host, err)
		}
	}
}

func TestReorderArgs(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	memory := fs.Int("memory", 0, "")
	attach := fs.Bool("attach", false, "")
	var spaces stringList
	fs.Var(&spaces, "s", "")

	got := reorderArgs(fs, []string{"work", "--memory", "1024", "-s", "base", "--attach", "extra"})
	want := []string{"--memory", "1024", "-s", "base", "--attach", "work", "extra"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
	if err := fs.Parse(got); err != nil {
		t.Fatal(err)
	}
	if *memory != 1024 || !*attach || len(spaces) != 1 || spaces[0] != "base" {
		t.Fatalf("parsed memory=%d attach=%v spaces=%v", *memory, *attach, spaces)
	}
	if fs.NArg() != 2 || fs.Arg(0) != "work" || fs.Arg(1) != "extra" {
		t.Fatalf("positional = %v", fs.Args())
	}
}
