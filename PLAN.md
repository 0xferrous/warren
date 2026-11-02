# warren — minimal Go rewrite of ash

Status: **draft, revised after discovering the existing NixOS-on-microsandbox
workflow in `my-nix/config/microsandbox`.**

## Implementation status (2026-11-01)

Done and exercised on Linux x86_64 + KVM:

- `warren` v0.1.0, ~2.4k Go LOC, Nix package, guest-image package
- NixOS OCI image boot with systemd as PID 1 and agentd surviving
- persistent managed root disk across stop/start
- spawn from a flake target; in-guest `nixos-rebuild switch` path verified
  (error streaming; a full successful fetch/build is still pending)
- config spaces, files, env, cwd mount
- rootless hot mount/umount with bindfs staging, kernel-bind fallback, and
  reapply on start
- `attach` implemented; `run`, `cp`, `logs`, `pause`, `resume`,
  `inspect`, `ls`, `stop`, `start`, `rm` exercised
- tests, `go vet`, `nix build .#warren`, `nix flake check`

Still to do:

- exercise attach TTY and ephemeral cleanup interactively
- complete a real `nixos-rebuild` fetch/build end to end in CI
- optional host-built target images instead of in-guest rebuild
- `mount-space`/`umount-space`, snapshot/branch commands if wanted

## Decisions

1. NixOS guests are required.
2. Runtime: microsandbox, not virtle.
3. Reuse the ash config shape where cheap; no migration tool.
4. Hot mount is a v1 requirement.
5. New name: **warren** (binary `warren`).
6. Linux-only.

## The key simplification

Do **not** build a rootfs, synthetic Nix DB, or host-store mount.

NixOS already runs under microsandbox via a NixOS OCI image:

- image built with `pkgs.dockerTools.buildLayeredImageWithNixDb`
- `config.Entrypoint = [ "/init" ]`, `msb create --init auto`
- `/init` hands PID 1 to systemd; agentd survives as a child for exec/attach
- image `/init` is patched to follow `/nix/var/nix/profiles/system` when it
  exists, so a `nixos-rebuild switch` survives microsandbox restarts
- a managed root disk keeps `/nix/var/nix/profiles` and the rest of the
  writable root across stop/start

Reference implementation: `/home/agent/dev/fr/my-nix/config/microsandbox`.

That means `warren` is a CLI around:

```
build generic NixOS OCI image
        ↓
microsandbox image load / cache
        ↓
create sandbox (persistent root, /workspace, /persist, shares)
        ↓
nixos-rebuild switch --flake /workspace#HOST   (when a flake is given)
        ↓
attach / run / mount / stop / start / rm
```

Host-side Nix work is limited to building the generic image and, optionally,
building a target image. No host Nix store exposure, no QGA, no virtiofsd,
no bindfs for the base mounts.

## Guest image

Ship a small `guest/` flake in this repo, derived from the proven
`my-nix/config/microsandbox` setup:

- NixOS with `nix`, flakes, `git`, `nixos-rebuild`, `sudo`, CA certs
- `boot.isContainer = true`; DHCP, resolvconf, firewall, sysctl, serial getty
  disabled because microsandbox supplies network/kernel setup
- `/init` patched to follow `/nix/var/nix/profiles/system`
- bootstrap `agent` user (wheel, passwordless sudo) before first activation
- minimal `/etc/passwd`, `/etc/group`, `/etc/sudoers` for `/init`
- `PATH` includes `/run/current-system/sw/bin` and the agent profile
- `Entrypoint = [ "/init" ]`

Image build is cached in the microsandbox image cache by tag/digest. First run
builds it with Nix; later runs reuse it.

Target flakes must import a small `warrenGuest` NixOS module (same defaults as
`my-nix/config/microsandbox/module.nix`). The module is part of this repo and
is exported as `nixosModules.guest`. This is the one config requirement; the
alternative is auto-extending arbitrary flakes with `extendModules`, which is
more magic and harder to debug.

`warren` can also accept `--image REF` to use a prebuilt NixOS image and skip
the guest flake entirely.

## Spawn flow

```
warren spawn NAME -f FLAKE#HOST [-s SPACE]... [--mount-cwd] [--eval]
```

1. Load config, resolve spaces, compute mounts/files/env.
2. Ensure the base NixOS image is loaded in microsandbox.
3. `CreateSandbox` / `ConnectOrCreateSandbox` with:
   - `WithImage(baseImage)`
   - `WithInit(Init.Auto())` (image entrypoint is `/init`)
   - `WithRootDisk(RootDisk.Managed(size))` for persistence
   - `WithMemory`, `WithCPUs`, `WithEnv`, labels
   - `WithMounts`:
     - `/workspace` <- cwd or configured workspace (rw)
     - `/persist` <- `<state>/<name>/persist` (rw)
     - `/var/warren/shares` <- `<state>/<name>/shares` (rw) for hot mounts
     - space ro/rw mounts
4. If `-f` is given and the target is not already active (or `--eval`),
   exec as root:
   `nixos-rebuild switch --flake /workspace#HOST`
5. Record the resolved config in `<state>/<name>.json`.
6. `--attach` opens the shell.

`--eval` re-runs `nixos-rebuild switch` against the mounted flake and leaves
the root disk intact. No sandbox recreation, no image rebuild.

## Hot mount

microsandbox has no live volume-add API (`ModifyOptions` covers CPU/memory/
labels/env/secrets/workdir only), so use the pre-mounted share root:

```
<state>/<name>/shares  ->  /var/warren/shares
```

`warren mount NAME HOST[:GUEST] [--mode ro|rw]`:

1. Stage `HOST` under `shares/hot/<id>` with rootless bindfs
   (`--multithreaded --no-allow-other`, metadata caches off). If bindfs is
   missing, fall back to a kernel `mount --bind` (root or passwordless sudo).
   A symlink cannot work: the guest sees the link but cannot resolve the host
   target.
2. Guest: `mkdir -p GUEST && mount --bind/ro /var/warren/shares/hot/<id> GUEST`
   through agentd.
3. Persist desired mounts in state; reapply after start.
4. `warren umount` removes the guest mount, staging entry, and state.

## Commands (v1)

```
warren spawn [NAME] -f FLAKE#HOST [-s SPACE]... [--mount-cwd] [--eval]
warren attach [NAME]
warren run [NAME] -- CMD...
warren mount NAME HOST[:GUEST] [--mode ro|rw]
warren umount NAME HOST_OR_GUEST
warren start NAME
warren pause NAME
warren resume NAME
warren stop NAME...
warren rm NAME...
warren ls [--json]
warren inspect NAME
warren logs [-f] [-n N] NAME
warren cp [--from] NAME SRC DST
```

`mount-space`/`umount-space` can be aliases over `mount` once spaces resolve to
IDs.

## Config (ash-shaped)

`$XDG_CONFIG_HOME/warren/config.toml`

```toml
[global]
image = "warren-nixos:latest"     # optional override
memory = 4096
cpus = 2
root_disk_mib = 32768
default_spaces = ["base"]

[spaces.base]
extends = []
rw_mounts = ["~/dev/project:~/workspace/project"]
ro_mounts = ["~/.config/git:~/.config/git"]
files = ["~/.gitconfig"]
env = { EDITOR = "vim" }
```

Reuse `HOST_PATH[:GUEST_PATH]`, `~` expansion, deduplication, missing-path
warnings, and `extends`. Ignore unsupported ash keys with a debug warning.
No migration tool.

## State

`$XDG_STATE_HOME/warren/<name>.json` plus a lock file:

- resolved image, resources, flake, host, spaces, mounts, files, env
- persistent dirs: `<state>/<name>/persist`, `<state>/<name>/shares`
- labels on the sandbox: `warren.managed=true`, `warren.version`,
  `warren.flake`, `warren.target`
- no virtle manifest, no gcroots, no store images

## Architecture

```
warren/
├── main.go
├── internal/
│   ├── cli/        # flag.FlagSet commands
│   ├── config/     # ash-shaped TOML + spaces
│   ├── guest/      # image build/load, nixos-rebuild, /init contract
│   ├── vm/         # microsandbox SDK wrapper
│   ├── shares/     # hot-mount staging + guest bind mounts
│   └── state/      # XDG records, locks
├── guest/          # NixOS bootstrap flake + warrenGuest module
├── flake.nix       # buildGoModule, CGO
└── docs/
```

Dependencies: microsandbox Go SDK, one TOML parser, standard library.

## M0 results

Tested on Linux x86_64 with KVM, microsandbox 0.7.12, and a NixOS image built
from `my-nix/config/microsandbox` (the same contract now lives in `guest/`).

1. **Runtime smoke — passed.** Release `msb` + `libkrunfw`, Go SDK v0.7.12
   with its embedded FFI, CGO build (40 MiB binary).
2. **NixOS boot — passed.** OCI image loaded into microsandbox; `/init` hands
   PID 1 to systemd; agentd survives; `systemctl is-system-running` reports
   `running`; `/bin/sh` and `/run/current-system` appear after activation.
   A managed root disk survives stop/start (`/root`, `/persist`, profiles).
3. **Hot mount — passed rootless with bindfs.** A symlink under the share
   root is visible but cannot resolve a host path. bindfs with
   `--multithreaded --no-allow-other -o attr_timeout=0,entry_timeout=0,
   negative_timeout=0` stages a host path without root; the bindfs mount is
   visible through microsandbox's virtiofs backend and can be bind-mounted in
   the guest. A kernel `mount --bind` is the fallback. The share root must be
   outside `/run` because NixOS mounts tmpfs over `/run`; it is now
   `/var/warren/shares`.
4. **Attach — not yet exercised interactively in this environment.**
   `warren run` as the guest user, exit codes, and `cp` are verified.
5. **Packaging — passed.** `nix build .#warren` with `vendorHash` and CGO;
   `nix build .#guest-image` produces the gzipped Docker archive.
6. **NixOS rebuild — path verified, full fetch still slow.** A deliberately
   failing flake streams the error and exits non-zero. A real target was
   downloading cache.nixos.org closures during the spike; the in-guest
   rebuild workflow is the one already used by `my-nix/config/microsandbox`.

Other findings:

- `Image.Load` does not auto-gunzip; warren expands `.tar.gz` first.
- `CreateSandbox` returns before NixOS activation; warren polls
  `systemctl is-system-running`.
- `Sandbox.Close` stops a non-detached VM; warren creates detached VMs.
- The SDK runtime resolver expects `libkrunfw.so.5.6.1`; the normal installer
  creates that name.

## Milestones

### M1 — walking skeleton (done)
- config/state, `spawn` (base image, no flake), `attach`, `ls`, `stop`,
  `start`, `rm`, `inspect`, `run`
- `warrenGuest` module + `guest/` image

### M2 — flake targets and spaces (mostly done)
- `-f FLAKE#HOST`, in-guest `nixos-rebuild switch`, `--eval` (path verified)
- spaces, files, cwd, default spaces, `inspect`, `cp`, `logs`

### M3 — hot mount (done, rootless)
- share root at `/var/warren/shares`, bindfs staging, guest bind mount,
  persistence/reapply on start
- `mount`, `umount`
- kernel-bind fallback; `WARREN_HOTMOUNT` override

### M4 — lifecycle polish (in progress)
- `start`, `pause`/`resume`, ephemeral, `--replace`, signals, exit codes
  (implemented; attach TTY and ephemeral cleanup need more testing)
- docs, tests, flake packaging

## Success criteria

- `warren spawn --name work -f ../my-nix#agent-microsandbox; warren attach work`
  boots NixOS, activates the target config, and attaches as the user
- `stop`/`start` preserves the activated profile and root filesystem
- `warren mount work ~/dev/project` appears in the running guest and survives
  restart (reapplied)
- no QGA, virtiofsd, systemd-run, or host Nix store exposure required
- rootless hot mounts via bindfs; kernel-bind fallback
- `rm` removes sandbox, root disk, state, and persist/shares dirs

## Open risks

| Risk | Mitigation |
| --- | --- |
| Guest `nixos-rebuild` needs network/build time | base image has Nix; `--image` fast path; cache target images later |
| Managed root disk growth | `WithRootDisk` size + `Modify` grow-only |
| Host lacks setuid fusermount3 | documented `programs.fuse`; kernel-bind fallback |
| Attach quality / user login | `AttachShell`, `WithAttachUser`, SSH fallback |
| Target flake must import `warrenGuest` | document; or auto-extend later |
| FFI/runtime version coupling | pin SDK + release FFI; `EnsureRuntime` |
| NixOS services expect host | reuse proven `my-nix` module defaults |

## Open questions

1. `warren` is the working name; say if it should change.
2. `-f FLAKE#HOST` is the primary interface; `--image`/`--guest-image` are
   fast paths.
3. Target flakes must import `warren.nixosModules.guest`. Auto-extending
   arbitrary configs is possible with `extendModules` but is deferred.
4. Updates use in-guest `nixos-rebuild switch`, matching the proven
   `my-nix/config/microsandbox` workflow. Host-built target images are a
   later optimization.
