// Package cli implements the warren command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/0xferrous/warren/internal/config"
	"github.com/0xferrous/warren/internal/shares"
	"github.com/0xferrous/warren/internal/state"
	"github.com/0xferrous/warren/internal/vm"
)

// Version is set by the linker.
var Version = "dev"

const (
	appName        = "warren"
	defaultTimeout = 10 * time.Minute
)

// Run executes args (excluding the program name) and returns an exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage(os.Stdout)
		return 0
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "spawn":
		err = cmdSpawn(rest)
	case "attach":
		err = cmdAttach(rest)
	case "run":
		err = cmdRun(rest)
	case "ls", "list":
		err = cmdList(rest)
	case "start":
		err = cmdStart(rest)
	case "stop":
		err = cmdStop(rest)
	case "rm", "remove":
		err = cmdRemove(rest)
	case "inspect":
		err = cmdInspect(rest)
	case "mount":
		err = cmdMount(rest)
	case "pause":
		err = cmdPause(rest)
	case "resume":
		err = cmdResume(rest)
	case "logs":
		err = cmdLogs(rest)
	case "cp":
		err = cmdCp(rest)
	case "umount", "unmount":
		err = cmdUmount(rest)
	case "version", "--version", "-v":
		fmt.Println("warren", Version)
		return 0
	case "help", "--help", "-h":
		usage(os.Stdout)
		return 0
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warren: %v\n", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `warren — NixOS agent VMs on microsandbox

Usage:
  warren spawn [NAME] -f FLAKE#HOST [-s SPACE]... [--mount-cwd] [--attach]
  warren attach [NAME]
  warren run [NAME] -- CMD...
  warren start NAME
  warren stop NAME...
  warren rm NAME...
  warren ls [--json]
  warren inspect NAME
  warren mount NAME HOST[:GUEST] [--mode ro|rw]
  warren umount NAME HOST_OR_GUEST
  warren pause NAME
  warren resume NAME
  warren logs [-f] [-n N] NAME
  warren cp [--from] NAME SRC DST
  warren version

Environment:
  WARREN_CONFIG      config file (default ~/.config/warren/config.toml)
  WARREN_STATE_DIR   state directory (default ~/.local/state/warren)
  WARREN_MSB         explicit msb binary
  WARREN_LIBKRUNFW   explicit libkrunfw library
  MSB_HOME           microsandbox runtime home`)
}

func cmdSpawn(args []string) error {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		configPath = fs.String("config", "", "config file")
		flake      = fs.String("f", "", "flake path#nixosConfiguration")
		flakeLong  = fs.String("flake", "", "flake path#nixosConfiguration")
		image      = fs.String("image", "", "guest image reference")
		guestImage = fs.String("guest-image", "", "load a Docker/OCI image archive")
		user       = fs.String("user", "", "guest user")
		memory     = fs.Int("memory", 0, "memory MiB")
		cpus       = fs.Int("cpus", 0, "vCPUs")
		rootDisk   = fs.Int("root-disk-mib", 0, "persistent root disk MiB")
		mountCwd   = fs.Bool("mount-cwd", false, "mount the current directory at ~/workspace")
		attach     = fs.Bool("attach", false, "attach after spawn")
		replace    = fs.Bool("replace", false, "replace an existing sandbox")
		ephemeral  = fs.Bool("ephemeral", false, "remove the sandbox when it stops")
		eval       = fs.Bool("eval", false, "re-run nixos-rebuild for an existing VM")
	)
	var spaces stringList
	fs.Var(&spaces, "s", "config space (repeatable)")
	fs.Var(&spaces, "space", "config space (repeatable)")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if *flake == "" {
		*flake = *flakeLong
	}

	name := fs.Arg(0)
	if name == "" {
		name = state.DefaultName()
	}
	name = state.SanitizeName(name)

	existing, loadErr := state.Load(name)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if loadErr != nil {
		existing = nil
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *image != "" {
		cfg.Global.Image = *image
	}
	if *memory > 0 {
		cfg.Global.Memory = *memory
	}
	if *cpus > 0 {
		cfg.Global.CPUs = *cpus
	}
	if *rootDisk > 0 {
		cfg.Global.RootDiskMiB = *rootDisk
	}
	if *user != "" {
		cfg.Global.GuestUser = *user
	}
	guestUser := cfg.Global.GuestUser
	guestHome := cfg.Global.GuestHome
	if *user != "" {
		guestHome = "/home/" + guestUser
		if guestUser == "root" {
			guestHome = "/root"
		}
	}

	spaceNames := []string(spaces)
	if len(spaceNames) == 0 && existing != nil {
		spaceNames = existing.Spaces
	}
	resolved, err := cfg.Resolve(spaceNames)
	if err != nil {
		return err
	}
	for _, w := range resolved.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	flakeSpec := *flake
	if flakeSpec == "" && existing != nil && existing.Flake != "" {
		flakeSpec = existing.Flake + "#" + existing.Host
	}
	var flakeDir, flakeHost string
	if flakeSpec != "" {
		flakeDir, flakeHost, err = splitFlake(flakeSpec)
		if err != nil {
			return err
		}
		flakeDir = expandUserPath(flakeDir)
		flakeDir, err = filepath.Abs(flakeDir)
		if err != nil {
			return err
		}
	}
	cwd, _ := os.Getwd()

	workspace := flakeDir
	workspaceRO := false // nixos-rebuild may need to write flake.lock
	if workspace == "" && existing != nil && existing.Workspace != "" {
		workspace = existing.Workspace
		workspaceRO = existing.WorkspaceReadOnly
	}
	extraMounts := append([]config.Mount(nil), resolved.Mounts...)
	if workspace == "" {
		if *mountCwd {
			workspace = cwd
			workspaceRO = false
		}
	} else if *mountCwd && !samePath(cwd, flakeDir) {
		extraMounts = append(extraMounts, config.Mount{
			ID:    "cwd",
			Host:  cwd,
			Guest: filepath.Join(guestHome, "workspace"),
		})
	}

	rec := &state.Record{
		Name:              name,
		Image:             cfg.Global.Image,
		Flake:             flakeDir,
		Host:              flakeHost,
		User:              guestUser,
		Memory:            cfg.Global.Memory,
		CPUs:              cfg.Global.CPUs,
		RootDiskMiB:       cfg.Global.RootDiskMiB,
		Spaces:            resolved.Space,
		Workspace:         workspace,
		WorkspaceReadOnly: workspaceRO,
		Mounts:            extraMounts,
		Files:             resolved.Files,
		Env:               resolved.Env,
	}
	stateDir, err := state.Dir()
	if err != nil {
		return err
	}
	persistDir := filepath.Join(stateDir, name, "persist")
	sharesDir := filepath.Join(stateDir, name, "shares")

	unlock, err := state.Lock(name)
	if err != nil {
		return err
	}
	defer unlock()

	if existing != nil && !*replace && !*eval && existing.Activated {
		// Preserve the original creation config unless the user explicitly
		// asks to rebuild. Flake changes are applied by --eval.
		rec = existing
	}

	fmt.Fprintf(os.Stderr, "resolving microsandbox runtime...\n")
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := vm.EnsureRuntime(ctx); err != nil {
		return fmt.Errorf("microsandbox runtime: %w", err)
	}

	if *guestImage != "" {
		*guestImage = expandUserPath(*guestImage)
		fmt.Fprintf(os.Stderr, "loading guest image %s...\n", *guestImage)
		if err := vm.LoadImage(ctx, *guestImage, rec.Image); err != nil {
			return fmt.Errorf("load guest image: %w", err)
		}
	}

	if err := state.Save(rec); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "spawning %s...\n", name)
	sb, err := vm.Create(ctx, vm.CreateOptions{
		Record:            rec,
		Resolved:          resolved,
		Workspace:         workspace,
		WorkspaceReadOnly: workspaceRO,
		PersistDir:        persistDir,
		SharesDir:         sharesDir,
		Replace:           *replace,
		Ephemeral:         *ephemeral,
	})
	if err != nil {
		return err
	}
	defer sb.Close()

	if err := vm.WaitReady(ctx, sb, 2*time.Minute); err != nil {
		return err
	}
	if err := vm.ApplyFiles(ctx, sb, rec.Files, rec.User); err != nil {
		return err
	}
	if len(rec.HotMounts) > 0 {
		if err := shares.Reapply(ctx, sb, sharesDir, rec.HotMounts); err != nil {
			return err
		}
	}

	if rec.Flake != "" && (rec.Host != "") && (*eval || !rec.Activated) {
		fmt.Fprintf(os.Stderr, "activating %s from %s...\n", rec.Host, rec.Flake)
		if err := vm.Rebuild(ctx, sb, "/workspace", rec.Host, os.Stdout, os.Stderr); err != nil {
			return err
		}
		rec.Activated = true
		if err := state.Save(rec); err != nil {
			return err
		}
	}

	fmt.Printf("%s ready\n", name)
	if *attach {
		return attachTo(ctx, sb, rec)
	}
	return nil
}

func cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	user := fs.String("user", "", "guest user")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	name, rec, handle, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	if *user != "" {
		rec.User = *user
	}
	if handle.Status() != msb.SandboxStatusRunning {
		return fmt.Errorf("%s is %s; use `warren start %s`", name, handle.Status(), name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := handle.ConnectOrStart(ctx)
	if err != nil {
		return err
	}
	defer sb.Close()
	if err := vm.WaitReady(ctx, sb, 2*time.Minute); err != nil {
		return err
	}
	return attachTo(ctx, sb, rec)
}

func attachTo(ctx context.Context, sb *msb.Sandbox, rec *state.Record) error {
	home := rec.Home
	if home == "" {
		home = "/home/" + rec.User
		if rec.User == "root" {
			home = "/root"
		}
	}
	shell := rec.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	_, err := sb.AttachWith(ctx, shell, []string{"-l"},
		msb.WithAttachUser(rec.User),
		msb.WithAttachCwd(home),
	)
	return err
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	user := fs.String("user", "", "guest user")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	var name string
	if len(rest) > 0 && rest[0] != "--" {
		if _, err := state.Load(rest[0]); err == nil {
			name = rest[0]
			rest = rest[1:]
		}
	}
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return errors.New("usage: warren run [NAME] -- CMD...")
	}
	_, rec, handle, err := resolveOne(name)
	if err != nil {
		return err
	}
	if handle.Status() != msb.SandboxStatusRunning {
		return fmt.Errorf("sandbox is %s; use `warren start`", handle.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := handle.ConnectOrStart(ctx)
	if err != nil {
		return err
	}
	defer sb.Close()
	execUser := rec.User
	if *user != "" {
		execUser = *user
	}
	out, err := sb.Exec(ctx, rest[0], rest[1:], msb.WithExecUser(execUser))
	if err != nil {
		return err
	}
	if out.Stdout() != "" {
		fmt.Print(out.Stdout())
	}
	if out.Stderr() != "" {
		fmt.Fprint(os.Stderr, out.Stderr())
	}
	if out.ExitCode() != 0 {
		return fmt.Errorf("command exited %d", out.ExitCode())
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	records, err := state.List()
	if err != nil {
		return err
	}
	status := map[string]msb.SandboxStatus{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if page, err := msb.ListSandboxes(ctx); err == nil {
		for _, h := range page.Sandboxes {
			status[h.Name()] = h.Status()
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(records)
	}
	fmt.Printf("%-24s %-10s %-24s %s\n", "NAME", "STATUS", "IMAGE", "TARGET")
	for _, r := range records {
		st := status[r.Name]
		if st == "" {
			st = "-"
		}
		target := ""
		if r.Flake != "" {
			target = r.Flake + "#" + r.Host
		}
		fmt.Printf("%-24s %-10s %-24s %s\n", r.Name, st, r.Image, target)
	}
	return nil
}

func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	name, rec, handle, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := handle.ConnectOrStart(ctx)
	if err != nil {
		return err
	}
	defer sb.Close()
	if err := vm.WaitReady(ctx, sb, 2*time.Minute); err != nil {
		return err
	}
	if len(rec.HotMounts) > 0 {
		dir, _ := state.Dir()
		if err := shares.Reapply(ctx, sb, filepath.Join(dir, rec.Name, "shares"), rec.HotMounts); err != nil {
			return err
		}
	}
	fmt.Printf("%s running\n", name)
	return nil
}

func cmdStop(args []string) error {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "force stop")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		return errors.New("usage: warren stop NAME...")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	for _, name := range names {
		h, err := msb.GetSandbox(ctx, state.SanitizeName(name))
		if err != nil {
			return err
		}
		if h.Status() == msb.SandboxStatusStopped {
			continue
		}
		var opts []msb.StopOption
		if *force {
			_ = h.Kill(ctx, msb.WithKillTimeout(10*time.Second))
		} else {
			if err := h.Stop(ctx, opts...); err != nil {
				return err
			}
		}
		fmt.Printf("%s stopped\n", name)
	}
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "force removal")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	names := fs.Args()
	if len(names) == 0 {
		return errors.New("usage: warren rm NAME...")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	for _, raw := range names {
		name := state.SanitizeName(raw)
		rec, _ := state.Load(name)
		dir, _ := state.Dir()
		if rec != nil && len(rec.HotMounts) > 0 {
			if h, err := msb.GetSandbox(ctx, name); err == nil && h.Status() == msb.SandboxStatusRunning {
				if sb, err := h.ConnectOrStart(ctx, msb.WithConnectOrStartDetached()); err == nil {
					for _, m := range rec.HotMounts {
						_ = shares.Remove(ctx, sb, filepath.Join(dir, name, "shares"), m)
					}
					_ = sb.Close()
				}
			} else {
				for _, m := range rec.HotMounts {
					_ = shares.RemoveHost(filepath.Join(dir, name, "shares"), m)
				}
			}
		}
		if h, err := msb.GetSandbox(ctx, name); err == nil {
			if err := h.Destroy(ctx, msb.WithDestroyForce()); err != nil && !*force {
				return err
			}
		} else if !isNotFound(err) {
			return err
		}
		if err := state.Delete(name); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
		fmt.Printf("%s removed\n", name)
	}
	return nil
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		return errors.New("usage: warren inspect NAME")
	}
	r, err := state.Load(state.SanitizeName(name))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func cmdMount(args []string) error {
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mode := fs.String("mode", "rw", "ro or rw")
	user := fs.String("user", "", "guest user for ~ expansion")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: warren mount NAME HOST[:GUEST] [--mode ro|rw]")
	}
	name, rec, handle, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	if *user != "" {
		rec.User = *user
	}
	guestHome := rec.Home
	if guestHome == "" {
		guestHome = "/home/" + rec.User
		if rec.User == "root" {
			guestHome = "/root"
		}
	}
	readOnly := *mode == "ro"
	if *mode != "ro" && *mode != "rw" {
		return fmt.Errorf("invalid mode %q (expected ro or rw)", *mode)
	}
	m, warn, err := config.ParseMountSpec(fs.Arg(1), readOnly, guestHome)
	if err != nil {
		return err
	}
	if warn != "" {
		return errors.New(warn)
	}
	if vm.ReservedGuestPaths[m.Guest] {
		return fmt.Errorf("guest path %s is reserved", m.Guest)
	}
	for _, existing := range rec.HotMounts {
		if existing.Guest == m.Guest || existing.Host == m.Host {
			return fmt.Errorf("already mounted: %s -> %s", existing.Host, existing.Guest)
		}
	}
	if handle.Status() != msb.SandboxStatusRunning {
		return fmt.Errorf("%s is %s; start it before mounting", name, handle.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := handle.ConnectOrStart(ctx, msb.WithConnectOrStartDetached())
	if err != nil {
		return err
	}
	defer sb.Close()
	dir, _ := state.Dir()
	sharesDir := filepath.Join(dir, rec.Name, "shares")
	if err := shares.Ensure(ctx, sb, sharesDir, m); err != nil {
		return err
	}
	rec.HotMounts = append(rec.HotMounts, m)
	if err := state.Save(rec); err != nil {
		return err
	}
	fmt.Printf("%s -> %s\n", m.Host, m.Guest)
	return nil
}

func cmdUmount(args []string) error {
	fs := flag.NewFlagSet("umount", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: warren umount NAME HOST_OR_GUEST")
	}
	_, rec, handle, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	target := fs.Arg(1)
	abs, _ := filepath.Abs(target)
	var found *config.Mount
	for i := range rec.HotMounts {
		m := &rec.HotMounts[i]
		if m.Host == abs || m.Guest == target || m.ID == target {
			found = m
			break
		}
	}
	if found == nil {
		return fmt.Errorf("no hot mount matching %s", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if handle.Status() == msb.SandboxStatusRunning {
		sb, err := handle.ConnectOrStart(ctx, msb.WithConnectOrStartDetached())
		if err != nil {
			return err
		}
		defer sb.Close()
		dir, _ := state.Dir()
		if err := shares.Remove(ctx, sb, filepath.Join(dir, rec.Name, "shares"), *found); err != nil {
			return err
		}
	}
	out := rec.HotMounts[:0]
	for _, m := range rec.HotMounts {
		if m.ID != found.ID {
			out = append(out, m)
		}
	}
	rec.HotMounts = out
	if err := state.Save(rec); err != nil {
		return err
	}
	fmt.Printf("unmounted %s\n", found.Guest)
	return nil
}

func cmdPause(args []string) error {
	fs := flag.NewFlagSet("pause", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	name, _, h, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if err := h.Pause(ctx); err != nil {
		return err
	}
	fmt.Printf("%s paused\n", name)
	return nil
}

func cmdResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	name, _, h, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if err := h.Resume(ctx); err != nil {
		return err
	}
	fmt.Printf("%s resumed\n", name)
	return nil
}

func cmdLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	follow := fs.Bool("f", false, "follow")
	fs.BoolVar(follow, "follow", false, "follow")
	lines := fs.Int("n", 100, "lines")
	fs.IntVar(lines, "lines", 100, "lines")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	_, _, h, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	if h.Status() != msb.SandboxStatusRunning {
		// A stopped sandbox has persisted exec logs.
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		entries, err := h.Logs(ctx, msb.LogOptions{Tail: uint64(*lines)})
		if err != nil {
			return err
		}
		for _, e := range entries {
			_, _ = os.Stdout.Write(e.Data)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := h.ConnectOrStart(ctx, msb.WithConnectOrStartDetached())
	if err != nil {
		return err
	}
	defer sb.Close()
	cmd := fmt.Sprintf("journalctl -b --no-pager -n %d", *lines)
	if *follow {
		cmd += " -f"
	}
	stream, err := sb.ShellStream(ctx, cmd)
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch ev.Kind {
		case msb.ExecEventStdout, msb.ExecEventStderr:
			_, _ = os.Stdout.Write(ev.Data)
		case msb.ExecEventDone:
			return nil
		}
	}
}

func cmdCp(args []string) error {
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	from := fs.Bool("from", false, "copy from the guest to the host")
	_ = fs.Bool("r", false, "recursive (not supported yet)")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return errors.New("usage: warren cp [--from] NAME SRC DST")
	}
	_, _, h, err := resolveOne(fs.Arg(0))
	if err != nil {
		return err
	}
	if h.Status() != msb.SandboxStatusRunning {
		return fmt.Errorf("sandbox is %s; start it first", h.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	sb, err := h.ConnectOrStart(ctx, msb.WithConnectOrStartDetached())
	if err != nil {
		return err
	}
	defer sb.Close()
	if *from {
		return sb.FS().CopyToHost(ctx, fs.Arg(1), fs.Arg(2))
	}
	return sb.FS().CopyFromHost(ctx, fs.Arg(1), fs.Arg(2))
}

func resolveOne(name string) (string, *state.Record, *msb.SandboxHandle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if name != "" {
		name = state.SanitizeName(name)
		rec, err := state.Load(name)
		if err != nil {
			return "", nil, nil, err
		}
		h, err := msb.GetSandbox(ctx, name)
		if err != nil {
			return "", nil, nil, err
		}
		return name, rec, h, nil
	}
	records, err := state.List()
	if err != nil {
		return "", nil, nil, err
	}
	var matches []*state.Record
	for _, r := range records {
		h, err := msb.GetSandbox(ctx, r.Name)
		if err == nil && h.Status() == msb.SandboxStatusRunning {
			matches = append(matches, r)
		}
	}
	if len(matches) == 1 {
		r := matches[0]
		h, err := msb.GetSandbox(ctx, r.Name)
		return r.Name, r, h, err
	}
	if len(matches) == 0 {
		return "", nil, nil, errors.New("no running warren VM; pass a name")
	}
	return "", nil, nil, errors.New("multiple running warren VMs; pass a name")
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		path = configPath()
	}
	path = expandUserPath(path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return config.Default(), nil
	}
	return config.Load(path)
}

func configPath() string {
	if p := os.Getenv("WARREN_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "warren", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "warren", "config.toml")
}

func expandUserPath(p string) string {
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func splitFlake(s string) (path, host string, err error) {
	i := strings.LastIndexByte(s, '#')
	if i < 0 {
		return "", "", fmt.Errorf("flake %q must be PATH#NIXOS_CONFIGURATION", s)
	}
	path, host = s[:i], s[i+1:]
	if path == "" || host == "" {
		return "", "", fmt.Errorf("flake %q must be PATH#NIXOS_CONFIGURATION", s)
	}
	return path, host, nil
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return aa == bb
}

func isNotFound(err error) bool {
	var e *msb.Error
	return errors.As(err, &e) && e.Kind == msb.ErrSandboxNotFound
}

// reorderArgs lets flags appear after positional arguments, matching ash's
// CLI ergonomics. Go's flag package stops at the first non-flag otherwise.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				continue
			}
			f := fs.Lookup(name)
			if f == nil {
				continue
			}
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
