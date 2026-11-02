#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

echo "==> building warren"
nix build .#warren --out-link result-warren

echo "==> building the NixOS guest image"
nix build .#guest-image --out-link result-guest

echo "==> spawning the demo VM"
exec ./result-warren/bin/warren spawn demo \
  --guest-image "$(readlink -f result-guest)" \
  --image warren-nixos:latest \
  -f .#demo \
  --memory 2048 \
  --cpus 2 \
  --root-disk-mib 16384 \
  --attach "$@"
