{ pkgs, ... }:

{
  imports = [ ../guest/module.nix ];

  networking.hostName = "warren-demo";
  system.stateVersion = "26.05";

  time.timeZone = "UTC";

  nix = {
    enable = true;
    package = pkgs.nix;
    settings = {
      accept-flake-config = true;
      experimental-features = [ "nix-command" "flakes" ];
      trusted-users = [
        "root"
        "agent"
      ];
    };
  };

  environment.systemPackages = with pkgs; [
    bat
    cowsay
    curl
    fd
    file
    git
    hello
    htop
    jq
    less
    neovim
    python3
    ripgrep
    tmux
    tree
    wget
  ];

  environment.variables.EDITOR = "nvim";

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

  environment.etc."motd".text = ''
    warren demo VM

    You are the "agent" user with passwordless sudo.
    Try:
      hello
      python3 -c 'print("hello from python")'
      nix shell nixpkgs#go -c go version
      nix shell nixpkgs#nodejs -c node --version
      nix shell nixpkgs#figlet -c figlet warren

    The repo is mounted at /workspace when spawned with -f.
  '';
}
