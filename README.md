<div align="center">
  <img src="cpak-logo.svg#gh-light-mode-only" height="120">
  <img src="cpak-logo.svg#gh-dark-mode-only" height="120">
  <p>A portable, low-overhead application format for Linux.</p>
  <p>
    <a href="LICENSE">
      <img src="https://img.shields.io/badge/License-GPLv3-blue.svg" alt="License: GPLv3">
    </a>
  </p>
</div>

---

cpak installs applications from OCI images while keeping package metadata in a
Git repository. It provides native desktop integration, shared content-addressed
layers, atomic updates and a rootless Linux sandbox from one Go binary.

## Install

Download `cpak-linux-amd64` or `cpak-linux-arm64` and `SHA256SUMS` from the
[latest release](https://github.com/Containerpak/cpak/releases/latest), verify
the download, then install it in a directory on `PATH`:

```sh
sha256sum -c --ignore-missing SHA256SUMS
install -Dm755 cpak-linux-amd64 "$HOME/.local/bin/cpak"
cpak doctor
```

`cpak doctor` checks user namespaces, rootless OverlayFS, seccomp, Landlock,
cgroup delegation, display, audio and the host action broker. It
prints a JSON report with `cpak doctor --json`.

To build from source:

```sh
make all
```

The build produces one `cpak` binary and does not embed a second container
runtime.

Store application pages also provide a signed graphical installer. Each
download contains the matching cpak binary and the selected package identity.
The signed metadata also pins the SHA-256 of the complete installer. The
installer verifies both before writing cpak to `~/.local/bin` and installing the
application. A browser may save the file without its executable
bit; enable execution in the file manager or run `chmod +x` once before opening
it.

## Use

Install and run an application by its package repository:

```sh
cpak install github.com/bottlesdevs/bottles
cpak run github.com/bottlesdevs/bottles bottles
```

Other common operations:

```sh
cpak list
cpak update
cpak self-update --check
cpak stop github.com/bottlesdevs/bottles
cpak audit
cpak audit --repair
```

## Aliases

Aliases name installed applications without changing their stored origin:

```sh
cpak alias set bottles github.com/bottlesdevs/bottles
cpak run bottles bottles
cpak update bottles
cpak alias list --json
cpak alias remove bottles
```

Alias names are case-insensitive and stored as lowercase letters, digits and
hyphens. They are local to the cpak store and resolve only for installed
applications.

Applications can be pinned to a branch, release or commit. If none is selected,
cpak follows the `main` branch.

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
and declared features that cpak cannot apply are rejected.

```json
{
  "$schema": "https://raw.githubusercontent.com/Containerpak/cpak/v2/schema/manifest-v2.json",
  "manifest_version": "2.0",
  "name": "Example",
  "description": "Example application.",
  "version": "1.0.0",
  "image": "ghcr.io/example/example:main",
  "image_ref": "source",
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

Dependencies are installed with the application. A dependency uses `nested`
mode by default and runs as its own cpak through the parent service. Set its
mode to `layer` when the parent needs the dependency files in the same rootfs:

```json
"dependencies": [
  {"origin": "github.com/example/runtime", "mode": "layer"}
]
```

Layer dependencies do not export their binaries or permissions. Their OCI
layers are mounted below the parent image, so the parent owns the command,
configuration and complete permission policy. Addons remain optional and are
mounted above the parent image. The structured update result records every
effective permission change.

Set `image_ref` to `source` when CI publishes OCI tags for each Git branch,
release and commit. cpak then selects the matching tag for the requested Git
reference.

## Private registries

cpak pulls OCI manifests, indexes, configs and layers directly. It does not use
Docker, Podman, crane or container credential helpers. Bind credentials for a
private repository to its package origin:

```sh
cpak auth login github.com/example/private-app --username account
cpak auth status github.com/example/private-app
cpak auth logout github.com/example/private-app
```

Desktop sessions store secrets through Secret Service. With `--secret-file`,
cpak keeps the secret in the user-owned mode `0600` file and stores only its
absolute path in the binding. A binding is restricted to one package origin,
registry host and OCI repository path.

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
entry. `cpak dev` also launches the selected binary. Both use a temporary cpak
store and do not export files to the desktop or change installed applications.

## Runtime and sandbox

cpak creates user, mount, PID, IPC, UTS, cgroup and optional network namespaces
directly through the Linux kernel. A per-container PID 1 owns the lifecycle and
accepts bounded local execution requests over a private Unix socket. OverlayFS
combines immutable OCI layers with disposable runtime state.

The runtime applies `no_new_privs`, seccomp and Landlock where the host kernel
supports it. Filesystem paths, devices, sockets, networking, process sharing and
host actions are controlled by the manifest and user overrides. Nested user
namespaces remain blocked unless an application declares `userNamespaces`,
which lets browser sandboxes create their inner boundary.

Desktop notifications and external URIs use the system broker instead. It is
enabled with the `notification` and `openURI` permissions and exposes only the
matching shim. The application never receives the host D-Bus socket or command.

The same broker can expose a typed container provider without granting a host
shell:

```json
"hostActions": [
  {
    "provider": "containers",
    "capabilities": ["read", "manage-owned", "exec-owned"]
  }
]
```

`podman` and `docker` shims parse supported CLI operations inside the cpak and
send a closed request to the broker. Each shim selects its matching host engine.
Standard output, standard error, exit codes and cancellation are returned to the
caller. Read access can inspect host containers. Mutation and execution are
restricted to containers created by the requesting package and marked with its
ownership label. A nested container can mount only paths already granted to the
parent cpak, and a read-only grant cannot be promoted. Unsupported flags, host
namespaces, devices and privileged mode are rejected before the container
backend is started. There is no generic host command action.

Resource limits use delegated cgroup v2 controllers when available. Hosts
without a compatible cgroup manager can run applications without limits; a
requested limit fails with a direct diagnostic instead of being ignored.

## Desktop and kiosk sessions

A package can offer a complete Wayland desktop or a focused kiosk as a login
session while remaining usable as a normal application package:

```json
"sessions": [
  {
    "id": "dev.sinty.singularity",
    "name": "Singularity Desktop",
    "description": "Singularity Desktop session",
    "kind": "desktop",
    "entrypoint": "/usr/bin/singularity-session",
    "override": {
      "socketWayland": true,
      "deviceDri": true,
      "hostApplications": true,
      "filesystem": [
        {"path": "xdg-documents", "access": "read-write"},
        {"path": "xdg-download", "access": "read-write"}
      ]
    }
  }
]
```

Install the small system authority once, then register a session from an
installed package:

```sh
cpak system setup
cpak session list github.com/singularityos-lab/singularity-desktop
cpak session enable github.com/singularityos-lab/singularity-desktop dev.sinty.singularity
```

The system authority accepts only session registration and removal. Every
change passes through Polkit, every field is validated, and the display manager
entry calls a fixed cpak launcher with a registered identifier. Package paths or
commands never enter the privileged request. The desktop uses the same cpak
profile and user data as a windowed launch. Removing a package also removes the
sessions which no remaining installed version provides. `cpak system remove`
removes registered cpak sessions before uninstalling the authority.

## Store

cpak deduplicates package data at two levels. OCI layers are addressed by digest,
so an unchanged base or dependency layer is downloaded and stored once. FVS
then shares equal content-defined blocks across files and layers. Persistent
native checkouts reuse complete files through reflinks or hard links where the
filesystem supports them. DaBaDee remains available as a compatible storage
driver and as the explicit path deduplication tool.

Application launch reads an atomic index of prepared layer directories and
passes them directly to rootless OverlayFS. Storage preparation and publication
complete before the runtime index becomes active. Updating cpak prepares
existing stores before installing or updating an application. If that
preparation was interrupted, the next desktop launch shows progress, resumes
completed layers, and starts the application after publication.

Inspect, prepare, and verify the selected storage driver with:

```sh
cpak storage status
cpak storage migrate
cpak storage verify
cpak storage verify --repair
```

The storage driver protocol is versioned independently from cpak and uses a
private Unix socket. The built-in FVS and DaBaDee providers implement the same
contract. External drivers can use any language, but cpak accepts their paths
only below the configured driver root and refuses to start an external driver
when the host cannot confine it.

Installs and updates stage data before changing the active application record.
Interrupted updates are recovered on the next start, while garbage collection
retains every layer referenced by an installed package.

The package origin remains a normal Git repository, so the manifest can follow a
branch, release or immutable commit while the OCI digest records the exact image
that was installed.

## Documentation

The full user and package author documentation is available at
[cpak.it](https://cpak.it/).

## Contributing

Contributions are accepted under the [Contributor License Agreement](CLA.md).
See [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## License

cpak is available under the [GNU General Public License v3.0 only](LICENSE).
Non-exclusive commercial licenses are available directly from
[Mirko Brombin](https://bromb.in/). See
[COMMERCIAL-LICENSING.md](COMMERCIAL-LICENSING.md) for the licensing notice.
