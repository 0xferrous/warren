// Package config loads the ash-shaped warren configuration and resolves
// spaces into concrete mounts, files, and environment.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Defaults for a new installation.
const (
	DefaultImage       = "warren-nixos:latest"
	DefaultMemory      = 4096
	DefaultCPUs        = 2
	DefaultRootDiskMiB = 32768
	DefaultGuestUser   = "agent"
	DefaultGuestHome   = "/home/agent"
	DefaultShell       = "/bin/sh"
)

// Config is the top-level TOML document.
type Config struct {
	Global Global           `toml:"global"`
	Spaces map[string]Space `toml:"spaces"`
}

// Global holds defaults shared by every VM.
type Global struct {
	Image         string            `toml:"image"`
	Memory        int               `toml:"memory"`
	CPUs          int               `toml:"cpus"`
	RootDiskMiB   int               `toml:"root_disk_mib"`
	DefaultSpaces []string          `toml:"default_spaces"`
	GuestUser     string            `toml:"guest_user"`
	GuestHome     string            `toml:"guest_home"`
	Shell         string            `toml:"shell"`
	Workdir       string            `toml:"workdir"`
	Env           map[string]string `toml:"env"`
}

// Space is a named collection of mounts, files, and environment.
type Space struct {
	Extends  []string          `toml:"extends"`
	ROMounts []string          `toml:"ro_mounts"`
	RWMounts []string          `toml:"rw_mounts"`
	Files    []string          `toml:"files"`
	Env      map[string]string `toml:"env"`
}

// Mount is one resolved host-to-guest mount.
type Mount struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Guest    string `json:"guest"`
	ReadOnly bool   `json:"read_only"`
	Space    string `json:"space,omitempty"`
}

// File is one resolved host file to write into the guest.
type File struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Guest   string `json:"guest"`
	Mode    uint32 `json:"mode"`
	Space   string `json:"space,omitempty"`
	Content []byte `json:"-"`
}

// Resolved is the effective configuration for one VM.
type Resolved struct {
	Config *Config
	Space  []string
	Mounts []Mount
	Files  []File
	Env    map[string]string
	// Warnings contains non-fatal problems such as missing host paths.
	Warnings []string
}

// Default returns an empty configuration with defaults applied.
func Default() *Config {
	c := &Config{}
	c.applyDefaults()
	return c
}

// Load reads path. Unknown keys are ignored; callers can inspect warnings by
// decoding separately if needed.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := toml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) applyDefaults() {
	g := &c.Global
	if g.Image == "" {
		g.Image = DefaultImage
	}
	if g.Memory <= 0 {
		g.Memory = DefaultMemory
	}
	if g.CPUs <= 0 {
		g.CPUs = DefaultCPUs
	}
	if g.RootDiskMiB <= 0 {
		g.RootDiskMiB = DefaultRootDiskMiB
	}
	if g.GuestUser == "" {
		g.GuestUser = DefaultGuestUser
	}
	if g.GuestHome == "" {
		g.GuestHome = DefaultGuestHome
	}
	if g.Shell == "" {
		g.Shell = DefaultShell
	}
	if c.Spaces == nil {
		c.Spaces = map[string]Space{}
	}
}

// Resolve expands default and explicit spaces into mounts/files/env.
// Defaults are applied first, then explicit names, preserving order and
// deduplicating names.
func (c *Config) Resolve(explicit []string) (*Resolved, error) {
	names := append([]string(nil), c.Global.DefaultSpaces...)
	names = append(names, explicit...)
	names = dedupeStrings(names)

	order, err := c.spaceOrder(names)
	if err != nil {
		return nil, err
	}

	r := &Resolved{Config: c, Space: order, Env: map[string]string{}}
	for k, v := range c.Global.Env {
		r.Env[k] = v
	}

	seenMounts := map[string]bool{}
	seenFiles := map[string]bool{}
	for _, name := range order {
		sp := c.Spaces[name]
		for _, raw := range sp.ROMounts {
			m, warn, err := c.parseMount(raw, true, name)
			if err != nil {
				return nil, fmt.Errorf("space %q: %w", name, err)
			}
			if warn != "" {
				r.Warnings = append(r.Warnings, warn)
				continue
			}
			key := m.Host + "\x00" + m.Guest + "\x00" + fmt.Sprint(m.ReadOnly)
			if seenMounts[key] {
				continue
			}
			seenMounts[key] = true
			r.Mounts = append(r.Mounts, m)
		}
		for _, raw := range sp.RWMounts {
			m, warn, err := c.parseMount(raw, false, name)
			if err != nil {
				return nil, fmt.Errorf("space %q: %w", name, err)
			}
			if warn != "" {
				r.Warnings = append(r.Warnings, warn)
				continue
			}
			key := m.Host + "\x00" + m.Guest + "\x00" + fmt.Sprint(m.ReadOnly)
			if seenMounts[key] {
				continue
			}
			seenMounts[key] = true
			r.Mounts = append(r.Mounts, m)
		}
		for _, raw := range sp.Files {
			f, warn, err := c.parseFile(raw, name)
			if err != nil {
				return nil, fmt.Errorf("space %q: %w", name, err)
			}
			if warn != "" {
				r.Warnings = append(r.Warnings, warn)
				continue
			}
			key := f.Host + "\x00" + f.Guest
			if seenFiles[key] {
				continue
			}
			seenFiles[key] = true
			r.Files = append(r.Files, f)
		}
		for k, v := range sp.Env {
			r.Env[k] = v
		}
	}
	return r, nil
}

func (c *Config) spaceOrder(names []string) ([]string, error) {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var order []string
	var visit func(string, []string) error
	visit = func(name string, path []string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("space inheritance cycle: %s", strings.Join(append(path, name), " -> "))
		}
		sp, ok := c.Spaces[name]
		if !ok {
			return fmt.Errorf("unknown space %q", name)
		}
		state[name] = visiting
		for _, parent := range sp.Extends {
			if err := visit(parent, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = done
		order = append(order, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// ParseMountSpec parses HOST[:GUEST] for a one-off mount. guestHome expands a
// guest "~". It returns a warning string for a missing host path.
func ParseMountSpec(raw string, readOnly bool, guestHome string) (Mount, string, error) {
	c := &Config{Global: Global{GuestHome: guestHome}}
	c.applyDefaults()
	return c.parseMount(raw, readOnly, "")
}

func (c *Config) parseMount(raw string, readOnly bool, space string) (Mount, string, error) {
	host, guest, err := splitPair(raw)
	if err != nil {
		return Mount{}, "", err
	}
	host = expandHome(host, "")
	if abs, err := filepath.Abs(host); err == nil {
		host = abs
	}
	guest = expandHome(guest, c.Global.GuestHome)
	if !filepath.IsAbs(guest) {
		return Mount{}, "", fmt.Errorf("guest path %q must be absolute", guest)
	}
	info, err := os.Stat(host)
	if err != nil {
		if os.IsNotExist(err) {
			return Mount{}, fmt.Sprintf("skipping missing mount %s", host), nil
		}
		return Mount{}, "", err
	}
	if !info.IsDir() {
		return Mount{}, "", fmt.Errorf("mount source %s is not a directory", host)
	}
	return Mount{
		ID:       id("mount", host, guest, fmt.Sprint(readOnly)),
		Host:     host,
		Guest:    guest,
		ReadOnly: readOnly,
		Space:    space,
	}, "", nil
}

func (c *Config) parseFile(raw string, space string) (File, string, error) {
	host, guest, err := splitPair(raw)
	if err != nil {
		return File{}, "", err
	}
	host = expandHome(host, "")
	if abs, err := filepath.Abs(host); err == nil {
		host = abs
	}
	guest = expandHome(guest, c.Global.GuestHome)
	if !filepath.IsAbs(guest) {
		return File{}, "", fmt.Errorf("guest path %q must be absolute", guest)
	}
	info, err := os.Stat(host)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, fmt.Sprintf("skipping missing file %s", host), nil
		}
		return File{}, "", err
	}
	if !info.Mode().IsRegular() {
		return File{}, "", fmt.Errorf("file source %s is not a regular file", host)
	}
	content, err := os.ReadFile(host)
	if err != nil {
		return File{}, "", err
	}
	return File{
		ID:      id("file", host, guest),
		Host:    host,
		Guest:   guest,
		Mode:    uint32(info.Mode().Perm()),
		Space:   space,
		Content: content,
	}, "", nil
}

func splitPair(raw string) (host, guest string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty mount/file entry")
	}
	host, guest, _ = strings.Cut(raw, ":")
	if host == "" {
		return "", "", fmt.Errorf("empty host path in %q", raw)
	}
	if guest == "" {
		guest = host
	}
	return host, guest, nil
}

func expandHome(path, home string) string {
	if path == "~" {
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func id(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:8])
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// SortedSpaceNames returns all configured space names for diagnostics.
func (c *Config) SortedSpaceNames() []string {
	names := make([]string, 0, len(c.Spaces))
	for name := range c.Spaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
