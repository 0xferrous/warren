// Package vm wraps the microsandbox Go SDK for warren's lifecycle.
package vm

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/0xferrous/warren/internal/config"
	"github.com/0xferrous/warren/internal/state"
)

// ReservedGuestPaths must not be claimed by spaces or hot mounts.
var ReservedGuestPaths = map[string]bool{
	"/workspace":         true,
	"/persist":           true,
	"/var/warren/shares": true,
}

// RuntimeConfig mirrors the subset of SDK runtime selection warren exposes.
type RuntimeConfig struct {
	MSBPath       string
	LibkrunfwPath string
}

func runtimeConfig() RuntimeConfig {
	return RuntimeConfig{
		MSBPath:       os.Getenv("WARREN_MSB"),
		LibkrunfwPath: os.Getenv("WARREN_LIBKRUNFW"),
	}
}

// EnsureRuntime resolves or installs the microsandbox runtime.
func EnsureRuntime(ctx context.Context) (msb.ResolvedRuntime, error) {
	cfg := runtimeConfig()
	rt, err := msb.EnsureRuntime(ctx, msb.RuntimeConfig{
		MSBPath:       cfg.MSBPath,
		LibkrunfwPath: cfg.LibkrunfwPath,
	}, msb.InstallOptions{})
	if err != nil {
		return rt, err
	}
	if v := msbVersion(rt.MSBPath); v != "" && v != msb.SDKVersion() {
		return rt, fmt.Errorf("microsandbox runtime %s does not match SDK %s; install a matching runtime or remove the runtime home and retry", v, msb.SDKVersion())
	}
	return rt, nil
}

func msbVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 && f[0] == "msb" {
		return f[1]
	}
	return ""
}

// LoadImage loads a Docker/OCI archive into the microsandbox image cache.
// The SDK expects an uncompressed archive, so .gz/.tgz inputs are expanded
// to a temporary file first.
func LoadImage(ctx context.Context, path, tag string) error {
	loadPath := path
	cleanup := func() {}
	if gzipMagic(path) {
		f, err := os.CreateTemp("", "warren-image-*.tar")
		if err != nil {
			return err
		}
		loadPath = f.Name()
		cleanup = func() { _ = os.Remove(loadPath) }
		defer cleanup()
		if err := gunzipFile(path, f); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	tags := []string(nil)
	if tag != "" {
		tags = []string{tag}
	}
	_, err := msb.Image.Load(ctx, loadPath, tags...)
	return err
}

func gzipMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [2]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false
	}
	return b[0] == 0x1f && b[1] == 0x8b
}

func gunzipFile(src string, dst io.Writer) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	_, err = io.Copy(dst, gz)
	return err
}

// CreateOptions is the effective configuration for one VM.
type CreateOptions struct {
	Record            *state.Record
	Resolved          *config.Resolved
	Workspace         string // host directory bind-mounted at /workspace
	WorkspaceReadOnly bool
	PersistDir        string
	SharesDir         string
	Replace           bool
	Ephemeral         bool
	Labels            map[string]string
}

// Create starts or reuses the named sandbox.
func Create(ctx context.Context, opts CreateOptions) (*msb.Sandbox, error) {
	rec := opts.Record
	if rec == nil {
		return nil, errors.New("nil record")
	}
	mounts, err := buildMounts(opts)
	if err != nil {
		return nil, err
	}

	sandboxOpts := []msb.SandboxOption{
		msb.WithImage(rec.Image),
		msb.WithInit(msb.Init.Auto()),
		msb.WithRootDisk(msb.RootDisk.Managed(uint32(rec.RootDiskMiB))),
		msb.WithMemory(uint32(rec.Memory)),
		msb.WithCPUs(uint8(rec.CPUs)),
		msb.WithMounts(mounts),
		msb.WithShell(recUserShell(rec)),
		msb.WithDetached(),
		msb.WithLabels(labels(rec, opts.Labels)),
	}
	if len(rec.Env) > 0 {
		sandboxOpts = append(sandboxOpts, msb.WithEnv(rec.Env))
	}
	if opts.Ephemeral {
		sandboxOpts = append(sandboxOpts, msb.WithEphemeral(true))
	}

	if opts.Replace {
		if h, err := msb.GetSandbox(ctx, rec.Name); err == nil {
			_ = h.Destroy(ctx, msb.WithDestroyForce())
		} else {
			_ = msb.RemoveSandbox(ctx, rec.Name)
		}
		return msb.CreateSandbox(ctx, rec.Name, sandboxOpts...)
	}

	handle, err := msb.GetSandbox(ctx, rec.Name)
	if err == nil {
		return handle.ConnectOrStart(ctx, msb.WithConnectOrStartDetached())
	}
	if !isNotFound(err) {
		return nil, err
	}
	return msb.CreateSandbox(ctx, rec.Name, sandboxOpts...)
}

func recUserShell(rec *state.Record) string {
	if rec.Shell != "" {
		return rec.Shell
	}
	return "/bin/sh"
}

func buildMounts(opts CreateOptions) (map[string]msb.MountConfig, error) {
	rec := opts.Record
	mounts := map[string]msb.MountConfig{}
	if opts.Workspace != "" {
		mounts["/workspace"] = msb.Mount.Bind(opts.Workspace, msb.MountOptions{Readonly: opts.WorkspaceReadOnly})
	}
	if opts.PersistDir != "" {
		if err := os.MkdirAll(opts.PersistDir, 0o755); err != nil {
			return nil, err
		}
		mounts["/persist"] = msb.Mount.Bind(opts.PersistDir, msb.MountOptions{})
	}
	if opts.SharesDir != "" {
		if err := os.MkdirAll(opts.SharesDir, 0o755); err != nil {
			return nil, err
		}
		mounts["/var/warren/shares"] = msb.Mount.Bind(opts.SharesDir, msb.MountOptions{})
	}
	for _, m := range rec.Mounts {
		if ReservedGuestPaths[m.Guest] {
			return nil, fmt.Errorf("space mount target %s is reserved", m.Guest)
		}
		mounts[m.Guest] = msb.Mount.Bind(m.Host, msb.MountOptions{Readonly: m.ReadOnly})
	}
	return mounts, nil
}

func labels(rec *state.Record, extra map[string]string) map[string]string {
	out := map[string]string{
		"warren.managed": "true",
		"warren.version": "0.1.0",
	}
	if rec.Flake != "" {
		out["warren.flake"] = rec.Flake
	}
	if rec.Host != "" {
		out["warren.target"] = rec.Host
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func isNotFound(err error) bool {
	var e *msb.Error
	if errors.As(err, &e) {
		return e.Kind == msb.ErrSandboxNotFound
	}
	return false
}

// WaitReady waits for NixOS activation inside the guest. CreateSandbox returns
// when agentd is reachable, which can be before /bin/sh and systemd units are
// ready.
func WaitReady(ctx context.Context, sb *msb.Sandbox, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		out, err := sb.Exec(ctx, "/bin/sh", []string{
			"-c", `test -x /bin/sh && systemctl is-system-running 2>/dev/null | grep -Eq '^(running|degraded)$'`,
		})
		if err == nil && out.ExitCode() == 0 {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("systemd not ready (exit %d)", out.ExitCode())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("guest did not become ready: %w", last)
}

// ApplyFiles writes configured host files into the guest.
func ApplyFiles(ctx context.Context, sb *msb.Sandbox, files []config.File, user string) error {
	for _, f := range files {
		content := f.Content
		if content == nil {
			var err error
			content, err = os.ReadFile(f.Host)
			if err != nil {
				return fmt.Errorf("read %s: %w", f.Host, err)
			}
		}
		if err := sb.FS().Mkdir(ctx, parentDir(f.Guest)); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Guest, err)
		}
		if err := sb.FS().Write(ctx, f.Guest, content); err != nil {
			return fmt.Errorf("write %s: %w", f.Guest, err)
		}
		owner := "root:root"
		if user != "" && strings.HasPrefix(f.Guest, "/home/"+user+"/") {
			owner = user + ":users"
		}
		script := fmt.Sprintf("chmod %04o %s && chown %s %s",
			f.Mode, shellQuote(f.Guest), shellQuote(owner), shellQuote(f.Guest))
		out, err := sb.Exec(ctx, "/bin/sh", []string{"-c", script})
		if err != nil {
			return fmt.Errorf("chmod/chown %s: %w", f.Guest, err)
		}
		if out.ExitCode() != 0 {
			return fmt.Errorf("chmod/chown %s: exit %d: %s", f.Guest, out.ExitCode(), out.Stderr())
		}
	}
	return nil
}

// Rebuild runs nixos-rebuild switch against a flake mounted in the guest.
// flakeDir is the guest path; host is the nixosConfigurations attribute.
func Rebuild(ctx context.Context, sb *msb.Sandbox, flakeDir, host string, stdout, stderr io.Writer) error {
	if host == "" {
		return errors.New("flake target is empty")
	}
	script := fmt.Sprintf("cd %s && nixos-rebuild switch --flake %s",
		shellQuote(flakeDir), shellQuote(".#"+host))
	h, err := sb.ShellStream(ctx, script)
	if err != nil {
		return err
	}
	defer h.Close()
	for {
		ev, err := h.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		switch ev.Kind {
		case msb.ExecEventStdout:
			_, _ = stdout.Write(ev.Data)
		case msb.ExecEventStderr:
			_, _ = stderr.Write(ev.Data)
		case msb.ExecEventExited:
			if ev.ExitCode != 0 {
				return fmt.Errorf("nixos-rebuild exited %d", ev.ExitCode)
			}
		case msb.ExecEventDone:
			return nil
		}
	}
	return nil
}

func parentDir(path string) string {
	i := strings.LastIndexByte(path, '/')
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
