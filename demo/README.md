# warren demo VM

A small NixOS target for playing with warren. It imports
`../guest/module.nix`, gives the `agent` user passwordless sudo, and installs
a handful of everyday tools. Heavier toolchains are available on demand via
`nix shell`.

The demo is a **NixOS configuration in this repo**, not a separate flake. That
lets `warren spawn -f .#demo` mount the repo at `/workspace` and run
`nixos-rebuild switch --flake /workspace#demo` inside the guest.

## Quick start

From the repository root:

```sh
./demo/run.sh
```

The script builds `warren`, builds the guest image, and spawns/attaches the
demo VM. The first run also downloads the microsandbox runtime and image
layers, and the first activation fetches/builds the demo system inside the
guest, so it takes a while.

Manual equivalent:

```sh
nix build .#warren --out-link result-warren
nix build .#guest-image --out-link result-guest

./result-warren/bin/warren spawn demo \
  --guest-image "$(readlink -f result-guest)" \
  --image warren-nixos:latest \
  -f .#demo \
  --memory 2048 \
  --cpus 2 \
  --root-disk-mib 16384
```

Then:

```sh
./result-warren/bin/warren attach demo
```

On aarch64 use `-f .#demo-aarch64-linux`.

## Things to try

Inside the VM:

```sh
hello
python3 -c 'print("hello from python")'
nix shell nixpkgs#go -c go version
nix shell nixpkgs#nodejs -c node --version
git --version
nix shell nixpkgs#figlet -c figlet warren
```

From the host, with the VM running:

```sh
# one-off command as the agent user
warren run demo -- /bin/sh -c 'uname -a; id'

# copy files in and out
warren cp demo ./README.md /home/agent/README.md
warren cp --from demo /home/agent/README.md /tmp/README.md

# rootless hot mount (host needs programs.fuse.enable = true)
warren mount demo "$PWD:/home/agent/warren"
warren run demo -- ls /home/agent/warren
warren umount demo /home/agent/warren

# logs / lifecycle
warren logs -n 20 demo
warren stop demo
warren start demo
warren rm demo
```

The repo is already at `/workspace` because that is the flake path. The hot
mount above is useful for trying a second directory and for testing
`umount`/reapply behavior.

## Host requirement for rootless mounts

`warren mount` uses bindfs as the unprivileged user and needs a setuid
`fusermount3`:

```nix
programs.fuse.enable = true;
```

The warren Nix package wraps bindfs/fuse3 into its `PATH`; the setuid wrapper
must come from the host. Without it, warren falls back to a kernel
`mount --bind` (root or passwordless sudo), or you can force the failure with:

```sh
WARREN_HOTMOUNT=bindfs warren mount ...
```

## Notes

- The demo root disk is persistent across `stop`/`start`.
- `warren spawn demo --eval` re-runs `nixos-rebuild` against the current
  `demo/configuration.nix`.
- Change `demo/configuration.nix`, run `warren spawn demo --eval`, and the
  guest activates the new generation.
- `warren rm demo` removes the sandbox, root disk, and state.
