# VM lifecycle

warren is a thin lifecycle layer over microsandbox. The SDK owns the VM
process and its disk; warren owns the saved config, the flake activation, and
the hot-mount intent.

## State machine

```mermaid
stateDiagram-v2
    [*] --> Absent

    Absent --> Booting: warren spawn
    Booting --> Running: agentd + systemd ready
    Running --> Activating: first spawn or --eval
    Activating --> Running: nixos-rebuild switch

    Running --> Paused: warren pause
    Paused --> Running: warren resume

    Running --> Stopped: warren stop
    Paused --> Stopped: warren stop
    Stopped --> Booting: warren start
    Crashed --> Booting: warren start

    Running --> Crashed: VMM or guest failure
    Booting --> Crashed: boot failure

    Running --> Removing: warren rm
    Paused --> Removing: warren rm
    Stopped --> Removing: warren rm
    Crashed --> Removing: warren rm
    Removing --> Absent

    Running --> Removing: warren spawn --replace
    Stopped --> Removing: warren spawn --replace
    Paused --> Removing: warren spawn --replace
```

Notes:

- `Absent → Booting` also covers `warren spawn` reusing a stopped sandbox:
  microsandbox starts it through `ConnectOrStart`.
- `Activating` is a warren phase, not a microsandbox status. It runs
  `nixos-rebuild switch --flake /workspace#HOST` inside the guest.
- `warren attach` does not change state. It only connects to a running VM.
- `warren stop` keeps the managed root disk and the host `persist`/`shares`
  directories.
- `warren rm` force-destroys a running sandbox after tearing down hot mounts.
- microsandbox also has `created`, `starting`, and `draining` states; warren
  normally surfaces `running`, `paused`, `stopped`, and `crashed`.

## `warren spawn`

```mermaid
sequenceDiagram
    participant U as User
    participant W as warren
    participant S as state sidecar
    participant M as microsandbox SDK
    participant R as msb machine / libkrun
    participant G as NixOS guest

    U->>W: warren spawn demo -f .#demo
    W->>W: load config, resolve spaces/mounts/files
    W->>S: save resolved record
    W->>M: EnsureRuntime()
    M->>R: resolve matching msb + libkrunfw
    W->>M: CreateSandbox(image, /init, root disk, mounts)
    M->>R: fork/exec msb machine
    R->>G: boot kernel, /init hands PID 1 to systemd
    M-->>W: sandbox handle (agentd reachable)
    W->>M: WaitReady(): exec /bin/sh + systemctl
    G-->>W: running (or degraded)
    W->>M: ApplyFiles()
    W->>M: Reapply hot mounts
    W->>M: nixos-rebuild switch --flake /workspace#demo
    M->>G: exec nixos-rebuild
    G-->>W: switch complete
    W-->>U: ready
```

The VM is created detached, so closing the CLI does not stop it.

If the named sandbox already exists and is activated, `spawn` reuses it and
skips `nixos-rebuild` unless `--eval` is passed. `--replace` destroys the old
sandbox and creates a new one.

## Day-2 operations

```mermaid
sequenceDiagram
    participant U as User
    participant W as warren
    participant M as microsandbox SDK
    participant G as NixOS guest

    U->>W: warren attach demo
    W->>M: GetSandbox + ConnectOrStart(detached)
    M-->>W: running sandbox
    W->>M: AttachWith(shell, user)
    M->>G: PTY shell
    Note over U,G: shell exits, VM stays running

    U->>W: warren stop demo
    W->>M: Stop()
    M->>G: shut down VM
    Note over W: root disk and host state stay

    U->>W: warren start demo
    W->>M: ConnectOrStart(detached)
    M->>G: boot activated NixOS profile
    W->>M: WaitReady()
    W->>M: Reapply hot mounts
```

`run`, `cp`, `mount`, `logs`, `pause`, `resume`, and `attach` require a
running VM. `start` requires a stopped or crashed VM.

## Hot mount lifecycle

```mermaid
sequenceDiagram
    participant U as User
    participant W as warren
    participant B as bindfs
    participant S as state sidecar
    participant M as microsandbox SDK
    participant G as NixOS guest

    U->>W: warren mount demo /host/project:/home/agent/project
    W->>W: validate paths and compute mount ID
    W->>S: load record
    W->>B: bindfs --no-allow-other /host/project shares/hot/ID
    B-->>W: FUSE mount ready
    W->>M: exec: mount --bind /var/warren/shares/hot/ID GUEST
    M->>G: guest bind mount
    W->>S: append to hot_mounts
    W-->>U: mounted

    U->>W: warren umount demo /home/agent/project
    W->>M: exec: umount GUEST
    M->>G: unmount
    W->>B: fusermount3 -u shares/hot/ID
    W->>S: remove from hot_mounts
```

The host path is exposed through one pre-mounted share root:

```text
host path
  → bindfs at <state>/<name>/shares/hot/<id>
  → microsandbox in-process virtio-fs at /var/warren/shares
  → guest bind mount at GUEST
```

After `warren start`, every desired hot mount is recreated from
`hot_mounts` in the sidecar. `warren stop` leaves host staging in place;
`warren rm` unmounts both sides before deleting the state directory.
