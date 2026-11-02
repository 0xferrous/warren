{
  description = "warren — NixOS agent VMs on microsandbox";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];

      mkGuest =
        system:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            "${nixpkgs}/nixos/modules/virtualisation/docker-image.nix"
            ./guest/configuration.nix
          ];
        };

      mkDemo =
        system:
        nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [ ./demo/configuration.nix ];
        };

      mkImage =
        system:
        let
          pkgs = import nixpkgs { inherit system; };
          guest = mkGuest system;
        in
        pkgs.dockerTools.buildLayeredImageWithNixDb {
          name = "warren-nixos";
          tag = "latest";
          maxLayers = 125;
          contents = [ guest.config.system.build.toplevel ];
          extraCommands = ''
            rm -f etc
            mkdir -p etc proc sys dev home/agent
            chown 1000:100 home/agent
            chmod 0755 home/agent

            cat > etc/passwd <<'EOF'
            root:x:0:0:root:/root:/bin/sh
            agent:x:1000:100:agent:/home/agent:/bin/sh
            EOF
            cat > etc/group <<'EOF'
            root:x:0:
            wheel:x:10:agent
            users:x:100:agent
            EOF
            cat > etc/sudoers <<'EOF'
            Defaults env_reset
            root ALL=(ALL:ALL) ALL
            %wheel ALL=(ALL:ALL) NOPASSWD: ALL
            EOF
            chmod 0440 etc/sudoers
          '';
          config = {
            Env = [
              "PATH=/run/wrappers/bin:/run/current-system/sw/bin:/etc/profiles/per-user/agent/bin:/home/agent/.nix-profile/bin:/nix/var/nix/profiles/default/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
            ];
            Entrypoint = [ "/init" ];
          };
        };
    in
    {
      nixosModules.guest = import ./guest/module.nix;

      nixosConfigurations =
        nixpkgs.lib.genAttrs systems (system: mkGuest system)
        // {
          # The usual local demo target. On aarch64 use demo-aarch64-linux.
          demo = mkDemo "x86_64-linux";
          demo-aarch64-linux = mkDemo "aarch64-linux";
        };

      packages = nixpkgs.lib.genAttrs systems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          default = self.packages.${system}.warren;
          warren = pkgs.callPackage ./nix/warren.nix { };
          guest-image = mkImage system;
        }
      );

      formatter = nixpkgs.lib.genAttrs systems (
        system: (import nixpkgs { inherit system; }).nixfmt-rfc-style
      );
    };
}
