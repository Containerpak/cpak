<div align="center">
  <img src="cpak-logo.svg#gh-light-mode-only" height="120">
  <img src="cpak-logo.svg#gh-dark-mode-only" height="120">
  <p>A portable, low-overhead application format for Linux.</p>
  <p>
    <a href="LICENSE">
      <img src="https://img.shields.io/badge/License-LGPLv2.1-blue.svg" alt="License: LGPLv2.1">
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
cpak system setup
cpak doctor
```

`cpak doctor` checks user namespaces, rootless OverlayFS, seccomp, Landlock,
cgroup delegation, display, audio and the host action broker. It
prints a JSON report with `cpak doctor --json`.

On Ubuntu systems that restrict unprivileged user namespaces, `cpak system
setup` installs a profile for a root-owned cpak copy. cpak uses that copy for
runtime commands and keeps the system restriction active for other
applications. The AppArmor profile is skipped on hosts that do not need it.

On NixOS, add the flake module so the binary and system authority are installed
declaratively:

```nix
inputs.cpak.url = "github:Containerpak/cpak/v2";

imports = [ inputs.cpak.nixosModules.default ];
services.cpak.enable = true;
```

The flake also provides `packages.${system}.cpak`. `cpak system setup` checks
the module-managed integration instead of writing to `/etc`.

To build from source:

```sh
make all
```

The build produces one `cpak` binary and does not embed a second container
runtime. It embeds the Adwaita, GTK, KDE and Qt dialog adapters by default. Each
adapter is a small host process linked to the matching system toolkit; cpak
extracts only the selected adapter and falls back to its built-in interface if
the toolkit or adapter is unavailable.

Distributions can limit the embedded adapters without changing runtime code:

```sh
make all UI_ADAPTERS=adwaita DIALOG_BACKEND=adwaita
make all UI_ADAPTERS=kde,qt
make all UI_ADAPTERS=builtin
```

The matching Go build tags are `cpak_ui_adwaita`, `cpak_ui_gtk`, `cpak_ui_kde`,
`cpak_ui_qt` and `cpak_ui_builtin`. The Makefile compiles and embeds only the
selected adapter binaries before invoking `go build` with those tags. A package
that invokes Go directly can use the same tags and install its matching
`cpak-ui-*` executables below `/usr/libexec/cpak/ui` instead of embedding them.

At runtime, `CPAK_UI_ADAPTER` selects `auto`, `builtin`, `adwaita`, `gtk`, `kde`
or `qt`. A persistent system or user choice uses the same value in `cpak.json`:

```json
{
  "desktop": {
    "dialog_backend": "adwaita"
  }
}
```

The environment overrides configuration, configuration overrides the build
default, and an unavailable selection always falls back to the built-in dialog.

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
cpak remove github.com/bottlesdevs/bottles
cpak self-update --check
cpak stop github.com/bottlesdevs/bottles
cpak audit
cpak audit --repair
cpak ps
```

`cpak remove` uses the source selector of the sole installed copy. When more
than one branch, release or commit is installed, pass the matching `--branch`,
`--release` or `--commit`. Package files are removed automatically. The private
application home and persistent file grants are retained unless `--purge` is
specified:

```sh
cpak remove --purge github.com/bottlesdevs/bottles
```

`cpak gc --apply` clears unused shared storage and download cache. It is not
required to complete a removal and does not delete a retained application home.

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

## Persistent environments

Distribution packages can back named, mutable environments. Install the package
once, create an environment, then enter it by name:

```sh
cpak install github.com/containerpak/archlinux
cpak environment create --name arch --origin github.com/containerpak/archlinux
cpak environment shell --environment arch
```

Package-manager changes and the private home survive container stops and host
reboots. `cpak environment stop` ends the running container without deleting
that state. `cpak environment delete` removes the environment and its data.
Persistent environments require `unshare`, `newuidmap`, `newgidmap` and at
least 65536 subordinate user and group IDs for the current account.

```sh
cpak environment list
cpak environment inspect --environment arch
cpak environment processes --environment arch
cpak environment signal --environment arch --pid 1234 --signal TERM
cpak environment stop --environment arch
cpak environment delete --environment arch
```

An environment starts with the installed package policy. Its own policy may
remove permissions but cannot add permissions the package does not have. Inspect
the ceiling with `cpak environment permissions --environment arch`, then apply a
narrower JSON policy:

```sh
cpak environment policy --environment arch --policy policy.json
```

Updating the installed package moves the environment to the new package version
while retaining its writable state.

## Application services

A package can name a default application command in its manifest:

```json
"services": {
  "server": {
    "binary": "/usr/bin/example",
    "arguments": ["serve", "--port", "3000"]
  }
}
```

The binary must also appear in `binaries`. Run the command once with:

```sh
cpak run --service server github.com/example/server
```

Register a persistent service with the same command:

```sh
cpak service enable api github.com/example/server \
  --service server \
  --restart always \
  --health 'example health' \
  --health-interval 30
```

The service manager restores enabled services, starts dependencies first,
stops dependants in reverse order and applies the selected restart policy.
`never`, `on-failure` and `always` are supported. A health command runs inside
the application container. A service becomes ready after that command
succeeds.

Runtime configuration uses repeatable flags:

```sh
cpak service enable api github.com/example/server \
  --service server \
  --env MODE=production \
  --env-file /srv/example/server.env \
  --secret API_TOKEN=/srv/example/api-token
```

Environment files use literal `NAME=value` lines. Empty lines and lines that
start with `#` are ignored. Values passed with `--env` replace values loaded
from files. URL and DSN values may contain colons.

A secret source must be an absolute, regular file owned by the current user
and inaccessible to group and other users. It is mounted read-only at
`/run/secrets/NAME`. cpak stores the source path, not the file contents, and
does not include secret contents in logs, status or inspection output.

Use the lifecycle commands directly:

```sh
cpak service list
cpak service status api
cpak service restart api
cpak service stop api
cpak service start api
cpak service logs api
cpak service disable api
cpak service remove api
```

`cpak service setup` installs boot activation and reports the selected adapter.
cpak uses a user systemd unit with lingering when available, otherwise it uses
the user crontab. Graphical-login activation is used when neither option is
available. A manual init hook is always written to
`~/.config/cpak/service-start`. The next application launch also starts the
manager and restores enabled services. The service manager itself does not
require systemd or D-Bus.

Runtime inspection keeps container and application process state separate:

```sh
cpak ps
cpak status github.com/example/server
cpak inspect --instance api github.com/example/server
cpak health --instance api github.com/example/server
```

`cpak health` exits with status 1 when the selected process is stopped,
starting or unhealthy. Network is reported as `none`, `isolated` or `host`.
Listening TCP ports owned by the application processes are appended to the
network mode, for example `host:3000`.

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

## Manifest v3

Each package repository contains a strict `cpak.json` manifest. Unknown fields
and declared features that cpak cannot apply are rejected.

```json
{
  "$schema": "https://raw.githubusercontent.com/Containerpak/cpak/v2/schema/manifest-v3.json",
  "manifest_version": "3.0",
  "name": "Example",
  "description": "Example application.",
  "version": "1.0.0",
  "image": "ghcr.io/example/example@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "binaries": ["/usr/bin/example"],
  "services": {
    "server": {
      "binary": "/usr/bin/example",
      "arguments": ["serve", "--port", "3000"]
    }
  },
  "desktop_entries": ["/usr/share/applications/example.desktop"],
  "form_factors": ["desktop"],
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
cpak gen-schema --manifest-version 3.0 --output schema/manifest-v3.json
cpak migrate-manifest cpak.json
```

Graphical packages can declare any combination of `desktop`, `phone`, `tablet`,
`tv` and `watch` in `form_factors`. Package stores may use the list for device
filters. Leaving it out means that support was not declared.

Set `displayX11` when an application needs X11 compatibility. cpak starts one
nested display for the container and mounts only its socket and authority file.
It uses Xwayland on a Wayland session or Xephyr on an X11 session. The host X11
display is not exposed. cpak keeps the application window sized with the nested
display, forwards its title, icon and fullscreen state to the Xephyr window, and
stops the instance after its last application window closes.

Clipboard access through `displayX11` is explicit and directional:

```json
"clipboard": {
  "hostToApp": true,
  "appToHost": true
}
```

On X11, cpak copies approved text and image targets between the host and nested
displays. File and URI list targets are not copied. A transfer is limited to 16
MiB. The application still receives only the nested X11 socket and its private
cookie. The broker is part of the cpak binary, so the image needs no clipboard
tool or desktop service. Xwayland exposes both clipboard directions through the
Wayland compositor, so a Wayland launch requires both directions to be declared.

Set `socketWayland` to share the Wayland display. The compositor also mediates
clipboard access through that socket, so the install prompt discloses both.

Set `bluetooth` to expose the BlueZ service through a private system bus proxy.
The permission covers general BlueZ use, including discovery, pairing, GATT,
agents, profiles and file descriptor passing. Calls to other system services
and raw HCI access remain blocked. If BlueZ or the host system bus is absent,
the application starts without Bluetooth. cpak does not invoke `dbus-daemon`
or an external D-Bus proxy. Services outside the declared policy appear
unavailable instead of exposing the host bus.

`runtime_sources` adds checksum-pinned artifacts to a managed runtime layer.
Use the `dpkg` or `rpm` installer for native packages. Use `deb-extract` when a
Debian package must be unpacked without running maintainer scripts or checking
its dependency metadata. Use `tar` for a tar or gzip-compressed tar archive
whose paths are rooted at `/` inside the package.
Set `architecture` to `amd64` or `arm64` when a source only applies to one
architecture. cpak ignores the other source before downloading it.

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

Manifest v3 requires an immutable digest in `image`. Manifest v2 remains
supported for repositories that select OCI tags through `image_ref: source`.

## Private registries

cpak pulls OCI manifests, indexes, configs and layers directly. It does not use
Docker, Podman, crane or container credential helpers. Bind credentials for a
private repository to its package origin:

```sh
cpak auth login github.com/example/private-app --username account
cpak auth status github.com/example/private-app
cpak auth logout github.com/example/private-app
```

For GHCR, enter a GitHub personal access token as the password and keep the
GitHub username. `--token` is for a registry-issued bearer token and cannot be
combined with `--username`.

Desktop sessions store secrets through Secret Service when it is available. If
the session has no D-Bus or Secret Service provider, cpak writes the secret to
its private configuration directory with mode `0600`. This fallback needs no
keyring service. With `--secret-file`, cpak keeps using the user-owned mode
`0600` file and stores only its absolute path in the binding. A binding is
restricted to one package origin, registry host and OCI repository path.

## Private GitHub repositories

Use the authenticated GitHub CLI when `cpak.json` is in a private repository:

```sh
cpak auth login github.com/example/private-app --github
```

cpak reads the existing `gh auth` session. If no session exists, an interactive
login opens through `gh`. Access to the source is restricted to the exact
package origin. When that manifest points to GHCR, the same credential is also
bound to its exact OCI repository. An image hosted by another registry still
uses a separate `cpak auth login` command.

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

cpak creates user, mount, PID, IPC, UTS, cgroup and network namespaces directly
through the Linux kernel. Applications without network permission get only a
private loopback interface. Applications with network permission use
`slirp4netns` for outbound traffic without sharing the host namespace or its
loopback services. If the helper is not installed, cpak downloads the official
static build to its private cache and verifies its size and SHA-256 before use.
A per-container PID 1 owns the lifecycle and
accepts bounded local execution requests over a private Unix socket. OverlayFS
combines immutable OCI layers with disposable runtime state.

The runtime applies `no_new_privs`, seccomp and Landlock. An application does
not start if either kernel filter cannot be installed. Filesystem paths,
devices, sockets, networking, process sharing and host actions are controlled
by the manifest and user overrides. Nested user namespaces remain blocked
unless an application declares `userNamespaces`, which lets browser sandboxes
create their inner boundary.

### Managed host lockdown

`cpak system set-enforcement refuse` is an integrity control. On its own it is
not an application allowlist: a local user can install and enrol new software.
A managed host must also restrict package origins or publishers and require
publisher signatures.

For example, save this as `cpak-trust.json`:

```json
{"abi":1,"approved_origins":["github.com/example/app"]}
```

Apply the controls in this order:

```sh
cpak system set-trust cpak-trust.json
cpak system set-signatures required
cpak system set-enforcement refuse
cpak doctor
```

`cpak doctor` reports the active enforcement, signature and trust settings and
the number of installed applications that are not enrolled.

Filtered session bus access, Bluetooth and the file chooser use native proxies
exposed on private Unix sockets. The Bluetooth proxy talks only to BlueZ and is
started only for a package that declares `bluetooth`. cpak does not invoke a
D-Bus daemon or an external D-Bus proxy. The private desktop proxy exposes the
standard `org.freedesktop.appearance` settings so applications follow the host
light or dark preference. Desktop notifications and external URIs use the
system broker instead. It is enabled with the `notification` and `openURI`
permissions and exposes only the matching shim. The application never receives
a raw host D-Bus socket or command.

### File selection

Applications keep a private persistent home unless the manifest explicitly
mounts the host home. A read-write grant for `home`, `host` or `/` lets the
application change startup files that later run outside cpak as the user.
Packages can let users select host files without granting a directory in
advance:

```json
"filePicker": {
  "openFile": true,
  "openFolder": true,
  "saveFile": true,
  "persistent": true,
  "containingFolder": true
}
```

The desktop adapter uses the native file chooser. A selected file is mounted
read-only below `/run/cpak/grants` and the application receives that path. The
user can explicitly grant its containing folder when the package permits this
choice. Folder selections are read-only. Save destinations mount the selected
parent directory read-write. If the desktop chooser cannot show the scope and
lifetime choices, cpak uses the configured desktop dialog backend or its
built-in dialog.

Paths already exposed by `filesystem` keep their normal in-container location
and do not trigger another confirmation. The built-in confirmation follows the
host light or dark preference and accent color through
`org.freedesktop.appearance`.

Use `home/path` when an application only needs one portable path below the
user's home instead of the complete `home` scope:

```json
"filesystem": [
  {"path": "home/.local/share/example", "access": "read-write"}
]
```

Session grants disappear with the running environment. Persistent grants are
restored on later launches and can be inspected or revoked:

```sh
cpak grant list github.com/example/app
cpak grant manage github.com/example/app
cpak grant revoke github.com/example/app GRANT_ID
```

GTK applications use the same mechanism through their normal file chooser.
Other applications can call the policy-gated `cpak-file-picker` shim. The core
grant protocol uses a private Unix socket and does not depend on a desktop bus.
The broker authenticates the package token, validates the requested capability
and passes an opened file descriptor to the existing container namespace. The
runtime verifies that the selected object did not change before attaching a
restricted mount. It does not trust a host path supplied by the application.

File selection fails closed when no desktop session is available. Headless
packages must use paths declared in `filesystem` or receive data through a
separate typed integration instead of starting an interactive picker.

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

Environment frontends can request the cpak provider instead:

```json
"hostActions": [
  {
    "provider": "cpak",
    "capabilities": ["read", "manage", "exec"]
  }
]
```

The `cpak-host` shim accepts only discovery and persistent environment
operations. `read` lists packages, environments, permissions and processes;
`manage` installs distribution packages and changes their environments; `exec`
opens a command in a selected environment, with or without a terminal. Other
cpak commands and malformed argument shapes are rejected before the host binary
starts.

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

cpak is free software licensed under the
[GNU Lesser General Public License v2.1](LICENSE). Accepted contributions remain
available under LGPL-2.1-only.
