/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"syscall"

	"github.com/godbus/dbus/v5"
)

// An authority is started once and then stays, so upgrading cpak leaves the
// old one answering: the new client speaks to a service built from the binary
// it just replaced. That failed as a decoding error nobody could act on, and
// the remedy was a command the person at the keyboard had no way to guess.
//
// So nobody has to guess. A service that is no longer the installed binary
// refuses the request and steps aside, the caller asks again, and the bus or
// systemd starts the one that is on disk now. The upgrade heals itself, and
// the only cost is a second attempt nobody sees.

// errAuthorityStaleName is the wire name of that refusal. It travels as a
// distinct error because a caller has to tell "ask again" apart from "no".
const errAuthorityStaleName = "it.cpak.Error.Stale"

// ErrAuthorityStale is the refusal, seen from the caller.
var ErrAuthorityStale = errors.New("the running system authority is not the installed one")

// stepAside is what the service does after refusing. It is a variable so a
// test can watch it happen without ending the test binary.
var stepAside = func() { os.Exit(0) }

// installedBinaryIdentity names the file at a path by what the kernel calls
// it. Setup replaces the binary by rename, so the inode changes even when the
// path does not, and comparing paths would notice nothing.
func installedBinaryIdentity(path string) (uint64, uint64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(stat.Dev), stat.Ino, true
}

// runningIsInstalled reports whether this process is the binary the host has
// installed. A host with nothing installed answers true: there is no newer
// authority to step aside for, and refusing every request would be worse than
// the skew this guards against.
func runningIsInstalled() bool {
	installed, found := installedLayout()
	if !found {
		return true
	}
	wantDev, wantIno, ok := installedBinaryIdentity(installed.binary)
	if !ok {
		return true
	}
	haveDev, haveIno, ok := installedBinaryIdentity("/proc/self/exe")
	if !ok {
		return true
	}
	return haveDev == wantDev && haveIno == wantIno
}

// refuseIfStale is the guard every served request passes. It answers nil when
// the service may go on.
func refuseIfStale() *dbus.Error {
	if runningIsInstalled() {
		return nil
	}
	// The exit happens after the refusal is on its way, so the caller is told
	// to ask again rather than losing the connection under it.
	defer stepAside()
	return dbus.NewError(errAuthorityStaleName, []any{ErrAuthorityStale.Error()})
}

// staleOnBus reports whether an answer is that refusal, which is the one error
// worth trying again rather than reporting.
func staleOnBus(err error) bool {
	var busErr dbus.Error
	if !errors.As(err, &busErr) {
		return false
	}
	return busErr.Name == errAuthorityStaleName
}
