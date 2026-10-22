/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// ResolveFlatpakMigration plans the cpak installation of an application found
// in a host installation. The build is fetched again from the remote and
// verified against the key cpak trusts: the host deployment is only read to
// learn which application, branch and remote to ask for, never imported.
//
// A remote cpak already serves from the same address keeps the key cpak has
// for it, Flathub's above all. Only a remote cpak does not know is taken with
// the key the host installation trusts it through, and the plan says so.
func (c *Cpak) ResolveFlatpakMigration(app flatpak.InstalledApp) (*FlatpakPlan, error) {
	if app.Ref.Arch != flatpak.HostArch() {
		return nil, fmt.Errorf("%s is built for %s", app.Ref.ID, app.Ref.Arch)
	}
	location, err := app.Installation.RemoteURL(app.Origin)
	if err != nil {
		return nil, err
	}
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return nil, err
	}
	request := FlatpakRequest{ID: app.Ref.ID, Branch: app.Ref.Branch, Arch: app.Ref.Arch}
	if remote, known := remotes.ByURL(location); known {
		request.Remote = remote.Name
		return c.ResolveFlatpak(request)
	}
	if _, err := remotes.Get(app.Origin); err == nil {
		return nil, fmt.Errorf("cpak already has a different remote named %s", app.Origin)
	}
	key, err := app.Installation.RemoteKey(app.Origin)
	if err != nil {
		return nil, err
	}
	request.newRemote = &flatpak.Remote{Name: app.Origin, URL: location, Key: key}
	return c.ResolveFlatpak(request)
}

// InstalledFlatpak answers with the cpak installation a plan describes, once
// it is installed.
func (c *Cpak) InstalledFlatpak(plan *FlatpakPlan) (types.Application, error) {
	app, err := c.getStoredApplication(plan.Origin, plan.App.Ref.Branch, plan.App.Ref.Branch, "", "")
	if err != nil {
		return types.Application{}, err
	}
	if app.CpakId == "" {
		return types.Application{}, fmt.Errorf("%s is not installed", plan.App.Ref.ID)
	}
	return app, nil
}

// FlatpakDataMigration counts what was moved from ~/.var/app into the
// private home cpak gives the application.
type FlatpakDataMigration struct {
	Source  string
	Copied  int
	Kept    int
	Skipped int
	// HostHome is set when the application was granted the real home, which
	// cpak never writes the Flatpak data into.
	HostHome bool
	// AlreadyCopied is set when an earlier migration finished copying, and
	// nothing was copied again.
	AlreadyCopied bool
}

// flatpakDataMarker sits beside the private home, where the application
// cannot write: only a copy that finished leaves it.
const flatpakDataMarker = "flatpak-data-migrated"

// MigratedFlatpak answers with the cpak installation a host application was
// already migrated to. It reads only what is on this machine, so a run with
// no network still recognises what an earlier one finished.
func (c *Cpak) MigratedFlatpak(source flatpak.InstalledApp) (types.Application, bool) {
	location, err := source.Installation.RemoteURL(source.Origin)
	if err != nil {
		return types.Application{}, false
	}
	remotes, err := c.FlatpakRemotes()
	if err != nil {
		return types.Application{}, false
	}
	remote, known := remotes.ByURL(location)
	if !known {
		return types.Application{}, false
	}
	app, err := c.getStoredApplication(FlatpakOrigin(remote.Name, source.Ref.ID), source.Ref.Branch, source.Ref.Branch, "", "")
	if err != nil || app.CpakId == "" || app.Flatpak == nil {
		return types.Application{}, false
	}
	return app, true
}

// flatpakDataTargets places the directories Flatpak points the XDG variables
// at where an application finds them under a home of its own. The cache is
// left behind: it is rebuilt, and it is often the largest part. Anything
// else sits in ~/.var/app because the application wrote it under $HOME, so
// it keeps its name.
var flatpakDataTargets = map[string]string{
	"config": ".config",
	"data":   ".local/share",
	"cache":  "",
}

// MigrateFlatpakData copies the data a host Flatpak application kept under
// ~/.var/app/<id> into the private home of its cpak installation. Nothing in
// the private home is replaced: a file the application already wrote under
// cpak is newer than the Flatpak copy, so it is kept and counted.
//
// The copy is made once. A later call answers that it was already made: the
// Flatpak data stays where it was, and copying it again would bring back
// whatever the user deleted under cpak since.
func (c *Cpak) MigrateFlatpakData(app types.Application) (FlatpakDataMigration, error) {
	dataPath, err := c.applicationDataPath(app.CpakId)
	if err != nil {
		return FlatpakDataMigration{}, err
	}
	marker := filepath.Join(dataPath, flatpakDataMarker)
	if _, err := os.Lstat(marker); err == nil {
		return FlatpakDataMigration{AlreadyCopied: true, HostHome: filesystemIncludesHostHome(app.ParsedOverride)}, nil
	}
	result, err := c.copyFlatpakData(app)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(dataPath, 0o700); err != nil {
		return result, err
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return result, fmt.Errorf("record the migrated data: %w", err)
	}
	return result, nil
}

func (c *Cpak) copyFlatpakData(app types.Application) (FlatpakDataMigration, error) {
	if app.Flatpak == nil {
		return FlatpakDataMigration{}, errors.New("the application was not installed from Flatpak")
	}
	ref, err := flatpak.ParseRef(app.Flatpak.Ref)
	if err != nil {
		return FlatpakDataMigration{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return FlatpakDataMigration{}, fmt.Errorf("resolve user home: %w", err)
	}
	result := FlatpakDataMigration{Source: filepath.Join(home, ".var", "app", ref.ID)}
	if _, err := os.Lstat(result.Source); os.IsNotExist(err) {
		return result, nil
	}
	if filesystemIncludesHostHome(app.ParsedOverride) {
		result.HostHome = true
		return result, nil
	}
	privateHome, err := c.privateApplicationHome(app.CpakId)
	if err != nil {
		return result, err
	}
	source, err := os.OpenRoot(result.Source)
	if err != nil {
		return result, err
	}
	defer source.Close()
	destination, err := os.OpenRoot(privateHome)
	if err != nil {
		return result, err
	}
	defer destination.Close()

	entries, err := fs.ReadDir(source.FS(), ".")
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		target, mapped := flatpakDataTargets[entry.Name()]
		if !mapped {
			target = entry.Name()
		}
		if target == "" {
			continue
		}
		if err := copyFlatpakData(source, destination, entry.Name(), target, &result); err != nil {
			return result, fmt.Errorf("migrate %s: %w", filepath.Join(result.Source, entry.Name()), err)
		}
	}
	return result, nil
}

// copyFlatpakData copies a tree between two roots, so neither a link in the
// Flatpak data nor one already in the private home can send a write outside
// it. Links are recreated as they are, never followed; sockets, pipes and
// devices are left behind, and no set-id bit survives the copy.
func copyFlatpakData(source, destination *os.Root, from, to string, result *FlatpakDataMigration) error {
	return fs.WalkDir(source.FS(), from, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(to, strings.TrimPrefix(strings.TrimPrefix(name, from), "/"))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if _, err := destination.Lstat(target); err == nil {
			if entry.IsDir() {
				return nil
			}
			result.Kept++
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		switch {
		case entry.IsDir():
			return destination.MkdirAll(target, info.Mode().Perm()|0o700)
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := source.Readlink(name)
			if err != nil {
				return err
			}
			if err := destination.Symlink(link, target); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			if err := copyFlatpakFile(source, destination, name, target, info.Mode().Perm()); err != nil {
				return err
			}
			if err := destination.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
				return err
			}
		default:
			result.Skipped++
			return nil
		}
		result.Copied++
		return nil
	})
}

func copyFlatpakFile(source, destination *os.Root, from, to string, mode os.FileMode) error {
	input, err := source.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := destination.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		destination.Remove(to)
		return err
	}
	return output.Close()
}

// RemoveHostFlatpak uninstalls an application and its data from the host
// installation it was found in. Flatpak removes its own deployments, so its
// exports and triggers are kept consistent; cpak does not delete them under
// it.
//
// Flatpak can fail after the deployment is gone, when the session services it
// tells about the removal cannot be reached, so whether the application was
// removed is read from the installation rather than from its exit status.
func RemoveHostFlatpak(app flatpak.InstalledApp) (removed bool, err error) {
	if err = runHostFlatpak(app.Installation, "uninstall", "--noninteractive", "--delete-data", app.Ref.String()); err != nil {
		err = fmt.Errorf("uninstall %s: %w", app.Ref.ID, err)
	}
	return !app.Installation.Deployed(app.Ref.ID), err
}

// RemoveUnusedHostFlatpaks uninstalls the runtimes and extensions nothing in
// the host installation uses any more.
func RemoveUnusedHostFlatpaks(installation flatpak.Installation) error {
	return runHostFlatpak(installation, "uninstall", "--noninteractive", "--unused")
}

func runHostFlatpak(installation flatpak.Installation, arguments ...string) error {
	binary, err := exec.LookPath("flatpak")
	if err != nil {
		return errors.New("flatpak is not installed on this system")
	}
	scope := "--user"
	if installation.Kind == flatpak.SystemInstallation {
		scope = "--system"
	}
	command := exec.Command(binary, append([]string{arguments[0], scope}, arguments[1:]...)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
