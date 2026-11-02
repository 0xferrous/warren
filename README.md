# warren

NixOS agent VMs on [microsandbox](https://github.com/superradcompany/microsandbox).

`warren` is a small Go CLI that keeps the useful part of `ash` — named,
persistent NixOS agent VMs, a flake target, config spaces, and rootless hot
mounts — and drops the host-side QEMU/virtiofsd/QGA/systemd-unit plumbing.

Status: **working prototype**. The happy path has been exercised end to end on
Linux x86_64 with KVM:

- boot a NixOS OCI image under microsandbox with systemd as PID 1
- persistent root disk across stop/start
- `nixos-rebuild switch --flake` wiring inside the guest (a full
  successful fetch/build is still pending)
- spawn / run / mount / umount / pause / resume / logs / cp /
  stop / start / rm (`attach` is implemented but needs an interactive TTY)
- hot mounts through one pre-mounted share root

See [`PLAN.md`](./PLAN.md) for the design and remaining work.

## How it works

1. `guest/` builds a generic NixOS OCI image with `dockerTools.buildLayeredImageWithNixDb`.
   The image entrypoint is `/init`, which hands PID 1 to NixOS/systemd.
   microsandbox's agentd stays alive as a child for exec/attach.
2. `warren spawn` creates a microsandbox with a **managed persistent root disk**
   and bind mounts for the workspace, `/persist`, and `/var/warren/shares`.
3. If a flake is given, `warren` runs
   `nixos-rebuild switch --flake /workspace#HOST` inside the guest. The NixOS
   `init` is patched to follow `/nix/var/nix/profiles/system` after the switch,
   so later starts boot the activated configuration.
4. Hot mounts stage a host path under the pre-mounted share root and bind it
   to the guest target; desired mounts are reapplied after `warren start`.

No host Nix evaluation, QGA, virtiofsd, bridge setup, or systemd user units
are required. bindfs is needed only for rootless hot mounts.

## Requirements

- Linux with KVM (`/dev/kvm`)
- Go 1.22+ and CGO if building from source
- Nix if you build the guest image locally
- rootless hot mounts: `bindfs`, plus a setuid `fusermount3`
  (`programs.fuse.enable = true` on NixOS); the Nix package wraps bindfs and
  fuse3, but the host must provide the setuid wrapper
- kernel bind mounts fall back to root/passwordless `sudo` if bindfs is absent
- microsandbox runtime: `warren` calls `EnsureRuntime` and installs it into
  `~/.microsandbox` on first use

## Build

```sh
nix build .#warren
./result/bin/warren version

# guest image (gzipped Docker/OCI archive)
nix build .#guest-image
```

The Go SDK embeds the microsandbox FFI library, so the binary is about 40 MiB.
CGO is used only for `dlopen`/`dlsym`.

## Runtime

`warren` uses microsandbox SDK v0.7.12 and checks that the selected `msb`
binary reports the same version. `EnsureRuntime` installs the runtime into
`~/.microsandbox` on first use. If an older runtime is already there, remove
`~/.microsandbox` (or point `WARREN_MSB`/`WARREN_LIBKRUNFW` at a matching
pair) and retry.

`--guest-image` accepts gzipped Docker/OCI archives; it expands them to
`$TMPDIR` before loading, so keep enough temporary space for the uncompressed
image.

## Quick start

Build/load the guest image once:

```sh
nix build .#guest-image
warren spawn work \
  --guest-image ./result \
  --image warren-nixos:latest \
  -f ~/nix#agent
warren attach work
```

Or, if the image is already in the microsandbox cache:

```sh
warren spawn work -f ~/nix#agent
```

`-f` is `PATH#NIXOS_CONFIGURATION`. The flake directory is mounted at
`/workspace` and the target must import `warren.nixosModules.guest` (see
`guest/README.md`).

Subsequent spawns reuse the activated configuration. Use `--eval` to run
`nixos-rebuild` again:

```sh
warren spawn work --eval
```

## Demo VM

A ready-to-play NixOS target lives in [`demo/`](./demo/README.md):

```sh
./demo/run.sh
```

It builds warren and the guest image, spawns a VM named `demo`, activates
`.#demo`, and attaches. It includes the usual shell tools plus Python, and the `agent` user has
passwordless sudo. Use `nix shell nixpkgs#go` (or Node/GCC) inside for heavier
tooling.

```sh
./result-warren/bin/warren run demo -- hello
./result-warren/bin/warren mount demo "$PWD:/home/agent/warren"
./result-warren/bin/warren spawn demo --eval   # after editing demo/configuration.nix
./result-warren/bin/warren rm demo
```

## Commands

```
warren spawn [NAME] -f FLAKE#HOST [-s SPACE]... [--mount-cwd] [--attach]
                 [--eval] [--replace] [--ephemeral]
                 [--image IMG] [--guest-image ARCHIVE]
                 [--memory MiB] [--cpus N] [--root-disk-mib MiB]
warren attach [NAME]
warren run [NAME] -- CMD...
warren mount NAME HOST[:GUEST] [--mode ro|rw]
warren umount NAME HOST_OR_GUEST
warren pause NAME
warren resume NAME
warren start NAME
warren stop NAME...
warren rm NAME...
warren ls [--json]
warren inspect NAME
warren logs [-f] [-n N] NAME
warren cp [--from] NAME SRC DST
```

Flags may appear before or after the name.

`cp` currently supports regular files. `--from` copies guest -> host.

## Configuration

`$XDG_CONFIG_HOME/warren/config.toml`, falling back to
`~/.config/warren/config.toml`. The shape intentionally mirrors ash; see [`example_config.toml`](./example_config.toml).

```toml
[global]
image = "warren-nixos:latest"
memory = 4096
cpus = 2
root_disk_mib = 32768
guest_user = "agent"
guest_home = "/home/agent"
default_spaces = ["base"]

[global.env]
EDITOR = "vim"

[spaces.base]
extends = []
rw_mounts = ["~/dev/project:~/workspace/project"]
ro_mounts = ["~/.config/git:~/.config/git"]
files = ["~/.gitconfig"]

[spaces.full]
extends = ["base"]
env = { FOO = "bar" }
```

Mount/file entries are `HOST_PATH` or `HOST_PATH:GUEST_PATH`. Host `~` expands
to the host home; guest `~` expands to `guest_home`. Missing host paths are
warnings. Spaces support recursive `extends` with cycle detection.

State lives in `$XDG_STATE_HOME/warren/` (fallback
`~/.local/state/warren/`):

```
warren/<name>.json          # resolved config + hot mount intent
warren/<name>/persist/      # bind-mounted at /persist
warren/<name>/shares/       # bind-mounted at /var/warren/shares
```

## Hot mounts

The guest gets one pre-mounted share root at `/var/warren/shares`. `warren
mount` stages the host path under `shares/hot/<id>` with **bindfs** and
bind-mounts `/var/warren/shares/hot/<id>` at the guest target.

Bindfs runs as the invoking user. It uses `--multithreaded
--no-allow-other` with metadata caches disabled; msb and its virtiofs backend
run as the same user, so no `allow_other` or `/etc/fuse.conf` entry is needed.
The host must provide a setuid `fusermount3`; on NixOS:

```nix
programs.fuse.enable = true;
```

The Nix package includes bindfs and fuse3 in the wrapped `PATH`, with the
host PATH taking precedence so `/run/wrappers/bin/fusermount3` wins.

If bindfs is unavailable, `warren` falls back to a kernel `mount --bind`
(root or passwordless `sudo`). Force a mode with `WARREN_HOTMOUNT=bindfs` or
`WARREN_HOTMOUNT=bind`. A symlink under the share root does not work because
the guest cannot resolve host paths.

## Differences from ash

Kept:

- named persistent VMs, flake target, spaces, `mount-cwd`, `cp`, logs,
  pause/resume, inspect/list
- ash-shaped config

Dropped (for now):

- Agent Portal, `gh`/`wl-paste` wrappers, D-Bus proxy
- Waypipe/Kitty/mDNS
- host virtiofsd/QGA/systemd-run plumbing (bindfs is used only for rootless
  hot mounts)
- Nix store strategies, image cache, `rebuild-db`, reconcile
- `ssh-config`/`forward`/`unforward`
- `rebuild-db`, `regenerate`

## Development

```sh
go test ./...
go vet ./...
nix build .#warren
nix build .#guest-image
```

Runtime environment for local development without relying on the SDK's
downloader:

```sh
export WARREN_MSB=/path/to/msb
export WARREN_LIBKRUNFW=/path/to/libkrunfw.so
export MSB_HOME=/path/to/microsandbox-home
```

Other environment variables: `WARREN_CONFIG`, `WARREN_STATE_DIR`,
`WARREN_HOTMOUNT` (`auto`, `bindfs`, `bind`), `WARREN_BINDFS`.
