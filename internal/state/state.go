// Package state persists per-VM metadata under the XDG state directory.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/0xferrous/warren/internal/config"
)

// Record is the sidecar for one named VM. microsandbox owns the sandbox and
// its disks; this record keeps the resolved warren configuration and hot
// mount intent.
type Record struct {
	Name              string            `json:"name"`
	Image             string            `json:"image"`
	Flake             string            `json:"flake,omitempty"`
	Host              string            `json:"host,omitempty"`
	User              string            `json:"user"`
	Home              string            `json:"home,omitempty"`
	Shell             string            `json:"shell,omitempty"`
	Memory            int               `json:"memory"`
	CPUs              int               `json:"cpus"`
	RootDiskMiB       int               `json:"root_disk_mib"`
	Spaces            []string          `json:"spaces"`
	Workspace         string            `json:"workspace,omitempty"`
	WorkspaceReadOnly bool              `json:"workspace_read_only,omitempty"`
	Mounts            []config.Mount    `json:"mounts,omitempty"`
	Files             []config.File     `json:"files,omitempty"`
	HotMounts         []config.Mount    `json:"hot_mounts,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Activated         bool              `json:"activated"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// Dir returns the warren state directory.
func Dir() (string, error) {
	if d := os.Getenv("WARREN_STATE_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "warren"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "warren"), nil
}

// Path returns the JSON sidecar path for name.
func Path(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, SanitizeName(name)+".json"), nil
}

// Load reads the record for name.
func Load(name string) (*Record, error) {
	p, err := Path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &r, nil
}

// Save atomically writes r.
func Save(r *Record) error {
	p, err := Path(r.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	r.UpdatedAt = time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = r.UpdatedAt
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".warren-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// Delete removes the record for name.
func Delete(name string) error {
	p, err := Path(name)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List returns all records sorted by name.
func List() ([]*Record, error) {
	d, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d, e.Name()))
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		out = append(out, &r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Lock takes an exclusive advisory lock for name and returns an unlock func.
func Lock(name string) (func(), error) {
	p, err := Path(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// SanitizeName maps a VM name to a safe state-file and sandbox name.
func SanitizeName(name string) string {
	if name == "" {
		return "warren"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "warren"
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// DefaultName returns the current directory basename plus a timestamp.
func DefaultName() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "warren-" + time.Now().Format("20060102150405")
	}
	base := SanitizeName(filepath.Base(cwd))
	return fmt.Sprintf("%s-%s", base, time.Now().Format("20060102150405"))
}
