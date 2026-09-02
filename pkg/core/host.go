/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"io/fs"
	"strconv"
	"strings"
)

// Host is the machine a decision is made for, stated rather than discovered.
//
// The mount list depends on four things the runtime used to read directly: the
// user id, the home directory, a handful of environment variables and what is
// actually present at a path. Reading them here would tie the answer to
// whichever machine asked, which is wrong twice over: a browser has no such
// machine, and a test that wants to know what cpak does for uid 1000 with a
// Wayland socket should not have to become uid 1000 and start a compositor.
//
// A zero Host is usable and describes a machine with nothing on it: no
// environment, no matches, no files. Every function field may be nil.
type Host struct {
	// UID is the user the container is built for.
	UID int

	// Home is the home directory, as the runtime resolved it.
	Home string

	// Getenv answers an environment variable, empty when it is unset.
	Getenv func(name string) string

	// Glob expands a shell pattern the way filepath.Glob does.
	Glob func(pattern string) ([]string, error)

	// Exists reports whether a path is there at all.
	Exists func(path string) bool

	// IsSocket reports whether a path is there and is a unix socket.
	IsSocket func(path string) bool

	// ReadFile answers the contents of a file, and an error that is
	// fs.ErrNotExist where there is no such file. It is read for the session
	// configuration a portable filesystem scope resolves through, and for
	// nothing else.
	ReadFile func(path string) ([]byte, error)
}

func (h Host) uid() string {
	return strconv.Itoa(h.UID)
}

func (h Host) env(name string) string {
	if h.Getenv == nil {
		return ""
	}
	return h.Getenv(name)
}

func (h Host) glob(pattern string) []string {
	if h.Glob == nil {
		return nil
	}
	matches, err := h.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

func (h Host) exists(path string) bool {
	return h.Exists != nil && h.Exists(path)
}

func (h Host) isSocket(path string) bool {
	return h.IsSocket != nil && h.IsSocket(path)
}

// readFile answers a file, and reads a host that cannot read one as a host that
// does not have it.
func (h Host) readFile(path string) ([]byte, error) {
	if h.ReadFile == nil {
		return nil, fs.ErrNotExist
	}
	return h.ReadFile(path)
}

// homeMount is the home directory as a mount, which is a directory and so ends
// in a separator. A home nobody could resolve answers "/" here, exactly as it
// did when the runtime appended the separator itself.
func (h Host) homeMount() string {
	if strings.HasSuffix(h.Home, "/") {
		return h.Home
	}
	return h.Home + "/"
}
