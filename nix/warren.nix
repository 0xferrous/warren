{
  lib,
  buildGoModule,
  makeWrapper,
  bindfs,
  fuse3,
}:

buildGoModule {
  pname = "warren";
  version = "0.1.0";

  src = ../.;

  # The microsandbox Go SDK embeds its FFI shared library and uses cgo for
  # dlopen/dlsym only; there are no external libraries to link.
  proxyVendor = true;
  vendorHash = "sha256-TkSx8WpMb4W0LitnWDTQS0mrfPvZbuZ74Pd4PlUWy5k=";

  env.CGO_ENABLED = "1";

  nativeBuildInputs = [ makeWrapper ];
  postInstall = ''
    wrapProgram "$out/bin/warren" \
      --suffix PATH : ${lib.makeBinPath [ bindfs fuse3 ]}
  '';

  ldflags = [
    "-s"
    "-w"
    "-X github.com/0xferrous/warren/internal/cli.Version=0.1.0"
  ];

  meta = {
    description = "NixOS agent VMs on microsandbox";
    license = lib.licenses.mit;
    mainProgram = "warren";
  };
}
