<div align="center">
  <img src="cpak-logo.svg#gh-light-mode-only" height="120">
  <img src="cpak-logo.svg#gh-dark-mode-only" height="120">
  <p>A portable, low-overhead application format for Linux.</p>
  <p>
    <a href="https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md">
      <img src="https://img.shields.io/badge/License-FPAL_TCV_1.0-orange.svg" alt="License: FPAL-TCV-1.0">
    </a>
  </p>
</div>

---

Cpak installs applications from OCI images while keeping package metadata in a
Git repository. It provides native desktop integration, shared content-addressed
layers, atomic updates and a rootless Linux sandbox from one Go binary.

## Install

Download the binary for the current `v2` build from the
[continuous release](https://github.com/Containerpak/cpak/releases/tag/continuous),
then install it in a directory on `PATH`:

```sh
install -Dm755 cpak "$HOME/.local/bin/cpak"
cpak doctor
```

`cpak doctor` checks user namespaces, rootless OverlayFS, seccomp, Landlock,
cgroup delegation, display, audio and the controlled host command bridge. It
prints a JSON report with `cpak doctor --json`.

To build from source:

```sh
make all
```

The build produces one `cpak` binary and does not embed a second container
runtime.

## Use

Install and run an application by its package repository:

```sh
cpak install github.com/containerpak/bottles
cpak run github.com/containerpak/bottles bottles
```

Other common operations:

```sh
cpak list
cpak update
cpak stop github.com/containerpak/bottles
cpak audit
cpak audit --repair
```

## Aliases

Aliases name installed applications without changing their stored origin:

```sh
cpak alias set bottles github.com/containerpak/bottles
cpak run bottles bottles
cpak update bottles
cpak alias list --json
cpak alias remove bottles
```

Alias names are case-insensitive and stored as lowercase letters, digits and
hyphens. They are local to the Cpak store and resolve only for installed
applications.

Applications can be pinned to a branch, release or commit. If none is selected,
Cpak follows the `main` branch.

## Addons and SDKs

An application declares which optional packages it supports. Enabled addons are
mounted above the application layers without expanding its permissions. This is
useful for SDKs in an editor:

```sh
cpak addon list github.com/containerpak/vscode
cpak addon enable github.com/containerpak/vscode github.com/containerpak/sdk-go
cpak addon enable github.com/containerpak/vscode github.com/containerpak/sdk-node-lts
```

The selection belongs to that application. Disabling an addon rebuilds its
runtime view, and an addon cannot be removed while another installed package is
using it.

## Manifest v2

Each package repository contains a strict `cpak.json` manifest. Unknown fields
and declared features that Cpak cannot apply are rejected.

```json
{
  "$schema": "https://raw.githubusercontent.com/Containerpak/cpak/v2/schema/manifest-v2.json",
  "manifest_version": "2.0",
  "name": "Example",
  "description": "Example application.",
  "version": "1.0.0",
  "image": "ghcr.io/example/example:main",
  "binaries": ["/usr/bin/example"],
  "desktop_entries": ["/usr/share/applications/example.desktop"],
  "dependencies": [],
  "addons": [],
  "idle_time": 0,
  "override": {
    "socketWayland": true,
    "deviceDri": true,
    "filesystem": [{"path": "home", "access": "read-write"}],
    "network": true
  }
}
```

Create, validate and migrate manifests with the CLI:

```sh
cpak init --help
cpak validate cpak.json
cpak gen-schema --output schema/manifest-v2.json
cpak migrate-manifest cpak.json
```

Dependencies are installed with the application. Addons remain optional. The
structured update result records every effective permission change.

## Package development

Resolve the manifest and its dependency graph to immutable OCI digests:

```sh
cpak lock cpak.json
```

The resulting `cpak.lock.json` includes the validated dependency manifests, so
later checks do not follow moved image tags. Both development commands infer the
Git repository origin and use the lock file beside the manifest when present:

```sh
cpak test cpak.json
cpak test cpak.json --binary example -- --version
cpak dev cpak.json
```

`cpak test` installs the package and checks every declared binary and desktop
entry. `cpak dev` also launches the selected binary. Both use a temporary Cpak
store and do not export files to the desktop or change installed applications.

## Runtime and sandbox

Cpak creates user, mount, PID, IPC, UTS, cgroup and optional network namespaces
directly through the Linux kernel. A per-container PID 1 owns the lifecycle and
accepts bounded local execution requests over a private Unix socket. OverlayFS
combines immutable OCI layers with disposable runtime state.

The runtime applies `no_new_privs`, seccomp and Landlock where the host kernel
supports it. Filesystem paths, devices, sockets, networking, process sharing and
host commands are controlled by the manifest and user overrides. `hrun` exposes
only explicitly allowed host commands and validates the peer process before
execution.

Resource limits use delegated cgroup v2 controllers when available. Hosts
without a compatible cgroup manager can run applications without limits; a
requested limit fails with a direct diagnostic instead of being ignored.

## Store

Layers are addressed by digest and shared across packages. Installs and updates
stage data before changing the active application record. Interrupted updates
are recovered on the next start, while garbage collection retains every layer
referenced by an installed package.

The package origin remains a normal Git repository, so the manifest can follow a
branch, release or immutable commit while the OCI digest records the exact image
that was installed.

## Documentation

The full user and package author documentation is available at
[cpak.it](https://cpak.it/).
