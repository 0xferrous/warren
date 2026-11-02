// Package shares implements hot mounts through one pre-mounted share root.
package shares

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	msb "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/0xferrous/warren/internal/config"
)

// StagingPath returns the host path under the pre-mounted share root.
func StagingPath(sharesDir string, m config.Mount) string {
	return filepath.Join(sharesDir, "hot", m.ID)
}

// GuestStagingPath returns the same path as seen inside the guest.
func GuestStagingPath(m config.Mount) string {
	return "/var/warren/shares/hot/" + m.ID
}

// Ensure makes the mount visible in the guest. It is idempotent.
func Ensure(ctx context.Context, sb *msb.Sandbox, sharesDir string, m config.Mount) error {
	if err := stageHost(sharesDir, m); err != nil {
		return err
	}
	if err := mountGuest(ctx, sb, m); err != nil {
		_ = unstageHost(sharesDir, m)
		return err
	}
	return nil
}

// Remove unmounts the guest target and tears down host staging.
func Remove(ctx context.Context, sb *msb.Sandbox, sharesDir string, m config.Mount) error {
	var errs []error
	if err := umountGuest(ctx, sb, m.Guest); err != nil {
		errs = append(errs, err)
	}
	if err := unstageHost(sharesDir, m); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// RemoveHost tears down host staging without touching the guest. Use it when
// the sandbox is already stopped.
func RemoveHost(sharesDir string, m config.Mount) error {
	return unstageHost(sharesDir, m)
}

// Reapply restores every desired hot mount after a start.
func Reapply(ctx context.Context, sb *msb.Sandbox, sharesDir string, mounts []config.Mount) error {
	var errs []error
	for _, m := range mounts {
		if err := Ensure(ctx, sb, sharesDir, m); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Host, err))
		}
	}
	return errors.Join(errs...)
}

func stageHost(sharesDir string, m config.Mount) error {
	dst := StagingPath(sharesDir, m)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		// A disconnected FUSE mount can make MkdirAll fail with ENOTCONN.
		_ = fuseUnmount(dst)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
	}
	if isMountpoint(dst) {
		return nil
	}

	pref := hotmountPreference()
	var bindfsErr error
	if pref != "bind" {
		if bindfs := bindfsBinary(); bindfs != "" {
			if err := bindfsMount(bindfs, m.Host, dst, m.ReadOnly); err == nil {
				return nil
			} else {
				bindfsErr = err
			}
		} else if pref == "bindfs" {
			return errors.New("rootless hot mount needs bindfs on PATH (or set WARREN_BINDFS)")
		}
	}
	if pref == "bindfs" {
		return bindfsErr
	}
	if bindfsErr != nil {
		fmt.Fprintf(os.Stderr, "warning: bindfs staging failed: %v; trying kernel bind\n", bindfsErr)
	}

	if err := bindMount(m.Host, dst, m.ReadOnly); err != nil {
		if bindfsErr != nil {
			return fmt.Errorf("bindfs failed: %v; kernel bind failed: %w", bindfsErr, err)
		}
		return err
	}
	return nil
}

func unstageHost(sharesDir string, m config.Mount) error {
	dst := StagingPath(sharesDir, m)
	if err := unmount(dst); err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func mountGuest(ctx context.Context, sb *msb.Sandbox, m config.Mount) error {
	src := GuestStagingPath(m)
	script := fmt.Sprintf("mkdir -p %s && mountpoint -q %s || mount --bind %s %s",
		quote(m.Guest), quote(m.Guest), quote(src), quote(m.Guest))
	if m.ReadOnly {
		script += fmt.Sprintf(" && mount -o remount,ro,bind %s", quote(m.Guest))
	}
	out, err := sb.Exec(ctx, "/bin/sh", []string{"-c", script})
	if err != nil {
		return err
	}
	if out.ExitCode() != 0 {
		return fmt.Errorf("guest mount exited %d: %s", out.ExitCode(), strings.TrimSpace(out.Stderr()))
	}
	return nil
}

func umountGuest(ctx context.Context, sb *msb.Sandbox, guest string) error {
	out, err := sb.Exec(ctx, "/bin/sh", []string{"-c", "mountpoint -q " + quote(guest) + " && umount " + quote(guest) + " || true"})
	if err != nil {
		return err
	}
	if out.ExitCode() != 0 {
		return fmt.Errorf("guest umount exited %d: %s", out.ExitCode(), strings.TrimSpace(out.Stderr()))
	}
	return nil
}

// bindfsMount uses bindfs to export a host directory without root. The
// --no-allow-other mode keeps the mount accessible to the same user (msb and
// its virtiofs backend) and avoids requiring user_allow_other in fuse.conf.
func bindfsMount(bindfs, src, dst string, readOnly bool) error {
	args := bindfsArgs(readOnly)
	args = append(args, src, dst)
	cmd := exec.Command(bindfs, args...)
	cmd.Env = bindfsEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "Operation not permitted") {
			msg += " (the host needs a setuid fusermount3; on NixOS enable programs.fuse)"
		}
		return fmt.Errorf("%s %s: %w: %s", bindfs, strings.Join(args, " "), err, msg)
	}
	return nil
}

func bindfsArgs(readOnly bool) []string {
	args := []string{
		"--multithreaded",
		"--no-allow-other",
		"-o", "attr_timeout=0,entry_timeout=0,negative_timeout=0",
	}
	if readOnly {
		args = append(args, "-r")
	}
	return args
}

// bindfsEnv makes the binding user's PATH prefer NixOS's setuid fusermount3
// wrapper even when warren was started with a minimal environment. bindfs
// locates fusermount3 through PATH.
func bindfsEnv() []string {
	path := os.Getenv("PATH")
	if st, err := os.Stat("/run/wrappers/bin"); err == nil && st.IsDir() {
		path = "/run/wrappers/bin:" + path
	}
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + path
			return env
		}
	}
	return append(env, "PATH="+path)
}

func bindfsBinary() string {
	if p := os.Getenv("WARREN_BINDFS"); p != "" {
		return p
	}
	p, _ := exec.LookPath("bindfs")
	return p
}

func hotmountPreference() string {
	switch p := os.Getenv("WARREN_HOTMOUNT"); p {
	case "bindfs", "bind", "auto":
		return p
	default:
		return "auto"
	}
}

func bindMount(src, dst string, readOnly bool) error {
	if err := run("mount", "--bind", src, dst); err != nil {
		return fmt.Errorf("bind %s -> %s: %w", src, dst, err)
	}
	if readOnly {
		if err := run("mount", "-o", "remount,ro,bind", dst); err != nil {
			_ = unmount(dst)
			return fmt.Errorf("remount read-only %s: %w", dst, err)
		}
	}
	return nil
}

func unmount(path string) error {
	if fuseUnmount(path) {
		return nil
	}
	if !isMountpoint(path) {
		return nil
	}
	if err := run("umount", path); err == nil {
		return nil
	}
	if err := run("umount", "-l", path); err == nil {
		return nil
	}
	return fmt.Errorf("failed to unmount %s; still mounted", path)
}

// fuseUnmount tries the unprivileged FUSE unmount helpers first. This is what
// bindfs mounts need; root bind mounts simply fall through to umount.
func fuseUnmount(path string) bool {
	for _, name := range []string{"fusermount3", "fusermount"} {
		candidates := []string{"/run/wrappers/bin/" + name}
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
		for _, p := range candidates {
			if _, err := os.Stat(p); err != nil {
				continue
			}
			for _, args := range [][]string{{"-u", path}, {"-uz", path}} {
				if exec.Command(p, args...).Run() == nil {
					return true
				}
			}
		}
	}
	return false
}

func isMountpoint(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if data, err := os.ReadFile("/proc/self/mounts"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && unescapeMount(fields[1]) == abs {
				return true
			}
		}
	}
	return false
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

func run(name string, args ...string) error {
	if os.Geteuid() != 0 {
		if !sudoWorks() {
			return errors.New("kernel bind mount requires root or passwordless sudo; install bindfs and enable fusermount3 for rootless hot mounts")
		}
		args = append([]string{"-n", name}, args...)
		name = "sudo"
	}
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(append([]string{name}, args...), " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

var sudoOK *bool

func sudoWorks() bool {
	if sudoOK != nil {
		return *sudoOK
	}
	ok := exec.Command("sudo", "-n", "true").Run() == nil
	sudoOK = &ok
	return ok
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
