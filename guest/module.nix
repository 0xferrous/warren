{ lib, pkgs, ... }:

{
  # microsandbox supplies the kernel and boot-time network configuration.
  boot.isContainer = lib.mkDefault true;

  # microsandbox invokes the image-root /init directly instead of using a
  # bootloader. Follow the activated NixOS profile when one exists, while
  # retaining the image generation for the first boot before any switch.
  system.systemBuilderCommands = ''
    ${pkgs.gawk}/bin/awk -v fallback="$out" '
      /^systemConfig=/ {
        print "systemConfig=/nix/var/nix/profiles/system";
        print "if [ ! -x " "\"" "$systemConfig/init" "\"" " ]; then";
        print "    systemConfig=" fallback;
        print "fi";
        next;
      }
      { print }
    ' "$out/init" > "$out/init.tmp"
    mv "$out/init.tmp" "$out/init"
    ${pkgs.coreutils}/bin/chmod 0755 "$out/init"
  '';

  system.tools.nixos-rebuild.enable = lib.mkDefault true;

  # Keep the next rebuild possible even when a target flake forgets these.
  nix = {
    enable = lib.mkDefault true;
    settings = {
      accept-flake-config = lib.mkDefault true;
      experimental-features = lib.mkDefault [ "nix-command" "flakes" ];
    };
  };
  environment.systemPackages = lib.mkDefault (with pkgs; [ cacert git ]);

  # These services target devices or host facilities that microsandbox does
  # not provide. Keep them as ordinary defaults so a consuming config can
  # intentionally override them.
  systemd.services."serial-getty@hvc0".enable = lib.mkDefault false;
  systemd.services.systemd-sysctl.enable = lib.mkDefault false;
  systemd.services.dhcpcd.enable = lib.mkDefault false;
  systemd.services.resolvconf.enable = lib.mkDefault false;

  # microsandbox's guest init configures eth0, the default route, and
  # /etc/resolv.conf from its per-sandbox network payload.
  networking.useDHCP = lib.mkDefault false;
  networking.useHostResolvConf = lib.mkDefault false;
  networking.resolvconf.enable = lib.mkDefault false;
  networking.firewall.enable = lib.mkDefault false;

  # Preserve the NixOS CA set and add the per-sandbox microsandbox MITM CA.
  # Interception is optional, so this must not block multi-user.target.
  systemd.services.warren-ca-bundle = {
    description = "Trust the microsandbox MITM CA";
    wantedBy = [ "multi-user.target" ];
    after = [ "local-fs.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      if [ ! -s /.msb/tls/ca.pem ] || [ ! -s /etc/pki/tls/certs/ca-bundle.crt ]; then
        exit 0
      fi

      ${pkgs.coreutils}/bin/mkdir -p /run/warren
      ${pkgs.coreutils}/bin/cat \
        /etc/pki/tls/certs/ca-bundle.crt \
        /.msb/tls/ca.pem \
        > /run/warren/ca-bundle.crt

      ${pkgs.coreutils}/bin/rm -f /etc/ssl/certs/ca-certificates.crt
      ${pkgs.coreutils}/bin/ln -s \
        /run/warren/ca-bundle.crt \
        /etc/ssl/certs/ca-certificates.crt
    '';
  };

  # Re-run the one-shot setup after nixos-rebuild has regenerated /etc.
  system.activationScripts.warrenCaBundle = {
    text = ''
      ${pkgs.systemd}/bin/systemctl try-restart warren-ca-bundle.service || true
    '';
    deps = [ ];
  };
}
