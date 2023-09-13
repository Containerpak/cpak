<div align="center">
  <img src="cpak-logo.svg#gh-light-mode-only" height="120">
  <img src="cpak-logo.svg#gh-dark-mode-only" height="120">
  <p>A fast, decentralized, portable,  powerful and low-memory footprint package 
    format for Unix.</p>
</div>

---

cpak is meant to simplify the process of distributing software via OCI images,
and to integrate with the operating system, bringing the benefits of
containerization to the desktop, in a truly native fashion.

> **Note:**
> cpak is still in early development.

## Installation

cpak is standalone, and can be installed by downloading the latest release from
the releases page (when those will be available), or by building it from source.

### Building from source

cpak is written in Go, and can be built with the following command:

```sh
make all
```

This will generate a `cpak` binary in the current directory. Note that using
`go` directly is not recommended, as it will fail due to the missing
`rootlesskit.tar.gz` tarball, which cpak embeds.

The `cpak-test` script can be used as an alternative to build and run cpak
in one command, it requires the `rootlesskit.tar.gz` tarball to be present in
the `pkg/tools` directory.

## Usage

cpak has a command-line interface, which can be used to create, install and
remove packages, among other things.

```sh
Usage:
  cpak [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  install     Install a package from a remote Git repository
  install     Install a package from a remote Git repository
  list        List all installed packages
  remove      Remove a package installed from a remote Git repository
  run         Run a package from a remote Git repository
  shell       Shell into a package
  spawn       Spawn a new namespace

Flags:
  -h, --help      help for cpak
  -v, --version   version for cpak

Use "cpak [command] --help" for more information about a command.
```

## Technical details

### Container's lifecycle

cpak uses `rootlesskit` to spawn a new namespace, and then uses `unshare` to
enter it and run the cpan `spawn` command. This command is responsible for
creating the container's filesystem, mounting all the OCI image layers, setting
up the pivot_root and finally executing the requested command.

In cpak, containers are of volatile nature, and are destroyed as soon as the
main process (the `spawn` command) exits. This is done to ensure that the
container's filesystem is always in a clean state at each run.

The container always refers to an application, and is identified by its
internal Id.

Once a container is spawned, cpak will use it for all the subsequent commands
that require a container for that application, so that the container is not
recreated at each command execution, leading to a faster and more efficient
experience and letting the user request multiple instances of the same
applications simultaneously.

### Applications

An application, in the context of cpak, is an OCI image that contains one
or more software packages.

In cpak, applications are identified by their origin, which is the Git
repository URL from which the application was installed.

#### Versioning

cpak uses Git branches, tags and commits to identify the version of an
application. When installing an application, the user can specify the branch,
tag or commit to use, and cpak will use that to fetch the application's
manifest. By default, cpak will use the `main` branch if no version, branch or
tag is specified.

The user can install multiple versions of the same application, and can choose
which version to run when executing the `run` command, by specifying the
version's branch, tag or commit.

#### Manifest

The application's manifest is a JSON file that contains all the information
about the application:

> **Note:** 
> the manifest is still in early development, and is subject to change
> when [this issue](https://github.com/Containerpak/cpak/issues/1) will be
> resolved.

```json
{
  "name": "My application",
  "description": "My application's description",
  "version": "0.0.1",
  "image": "ghcr.io/my-org/my-app:latest",
  "binaries": ["/usr/bin/my-app"],
  "desktop_entries": ["/usr/share/applications/my-app.desktop"],
  "dependencies": ["my-dependency"],
  "future_dependencies": ["my-future-dependency"],
}
```

The manifest contains the following fields:

- `name`: the application's name
- `description`: the application's description
- `version`: the application's version (in that specific branch or tag)
- `image`: the application's OCI image [1]
- `binaries`: a list of binaries that the application provides
- `desktop_entries`: a list of desktop entries that the application provides
- `dependencies`: a list of applications that the application depends on
- `future_dependencies`: a list of applications that the application will
  depend on in the future

##### Dependencies

Dependencies are applications that the application depends on, and that must be
installed alongside the application. Dependencies are installed recursively,
meaning that if an application depends on another application, which in turn
depends on another application, all the dependencies will be installed.

Dependencies does not have to use the same OCI image as the application, and
can be installed from different Git repositories, by just specifying the
repository URL (origin) in the manifest.

Dependencies' exports (binaries and desktop entries) are then made available to
the application, so that it can use them.

##### Future dependencies

Future dependencies are applications that the application could depend on in
the future, but that are not required at the moment. Future dependencies are
not installed, but are listed in the manifest so that the user can install
them manually if needed.

For example, if an application depends on an IDE, but the user does not want
to install it, the IDE can be listed as a future dependency, so that the user
can install it later if needed, and choose which one to install.
