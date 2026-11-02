# warren guest

This directory contains the NixOS side of warren.

- `configuration.nix` builds the generic bootstrap image.
- `module.nix` is exported as `warren.nixosModules.guest` and must be imported
  by any flake target activated with `warren spawn -f`.
- `flake.nix` at the repository root builds `packages.<system>.guest-image`.

## Target flake

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    warren.url = "github:0xferrous/warren";
  };

  outputs = { self, nixpkgs, warren }: {
    nixosConfigurations.agent = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        warren.nixosModules.guest
        ({ pkgs, ... }: {
          networking.hostName = "agent";
          system.stateVersion = "26.05";

          users.users.agent = {
            isNormalUser = true;
            uid = 1000;
            group = "users";
            home = "/home/agent";
            createHome = true;
            extraGroups = [ "wheel" ];
            hashedPassword = "!";
          };
          security.sudo.wheelNeedsPassword = false;

          nix = {
            enable = true;
            settings.experimental-features = [ "nix-command" "flakes" ];
          };

          environment.systemPackages = with pkgs; [
            git
            nix
          ];
        })
      ];
    };
  };
}
```

Then:

```sh
warren spawn work -f .#agent
```

The module provides:

- `boot.isContainer = true` for running without a bootloader or stage 1
- systemd/network defaults suitable for microsandbox's kernel and NIC
- a patched `/init` that follows `/nix/var/nix/profiles/system` after
  `nixos-rebuild switch`
- a CA bundle service that preserves microsandbox's optional MITM CA
- `nixos-rebuild` kept in the system

## Bootstrap image

`nix build .#guest-image` produces a gzipped Docker archive. `warren spawn
--guest-image ./result` expands and loads it into the microsandbox image cache,
then creates the sandbox with the image's `/init` entrypoint.

The image contains:

- Nix with flakes
- Git, CA certs, sudo, home-manager, nh
- an `agent` user with passwordless wheel sudo
- `/init` patched to follow the activated profile
- a minimal `/etc/passwd`, `/etc/group`, and `/etc/sudoers` so `/init` can
  resolve before the first NixOS activation

The bootstrap account can be replaced by any target flake that imports the
module.
