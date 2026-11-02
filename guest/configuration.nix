{ lib, pkgs, ... }:

{
  imports = [ ./module.nix ];

  networking.hostName = "warren";

  nix = {
    enable = true;
    package = pkgs.nix;
    settings = {
      accept-flake-config = true;
      experimental-features = [
        "nix-command"
        "flakes"
      ];
    };
  };

  # Keep only the installer tool needed for the next rebuild, and make it
  # explicit rather than relying on nix.enable's default.
  system.disableInstallerTools = true;
  system.tools.nixos-rebuild.enable = true;

  environment.systemPackages = with pkgs; [
    cacert
    git
    home-manager
    nh
    nix
    sudo
  ];

  # A usable bootstrap account before a replacement flake is activated.
  users.users.agent = {
    isNormalUser = lib.mkDefault true;
    uid = lib.mkDefault 1000;
    group = lib.mkDefault "users";
    home = lib.mkDefault "/home/agent";
    createHome = lib.mkDefault true;
    extraGroups = lib.mkDefault [ "wheel" ];
    hashedPassword = lib.mkDefault "!";
  };
  security.sudo.wheelNeedsPassword = lib.mkDefault false;

  system.stateVersion = "26.05";
}
