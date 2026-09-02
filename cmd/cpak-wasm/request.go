/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/core"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// response is what every call answers with. A call never throws and never
// returns a bare value: a caller reads ok first and then one of the other two
// fields, so a failure inside the runtime and a manifest that is simply wrong
// arrive the same shape.
type response struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// hostRequest describes the machine a policy is computed for.
//
// A browser has no such machine, so the caller states one. Paths and sockets
// are what the described host has on disk: globs match against both, and a
// permission whose socket is not listed produces no mount, exactly as it would
// on a real host where the socket is not there. Files are the few whose
// contents a decision reads, which today is the session's user directories.
type hostRequest struct {
	UID     int               `json:"uid"`
	Home    string            `json:"home"`
	Env     map[string]string `json:"env,omitempty"`
	Paths   []string          `json:"paths,omitempty"`
	Sockets []string          `json:"sockets,omitempty"`
	Files   map[string]string `json:"files,omitempty"`
}

// defaultHost is used when a caller describes no host. It is answered back in
// every reply that used it, so a result is never quietly about a machine the
// caller did not have in mind.
var defaultHost = hostRequest{UID: 1000, Home: "/home/user"}

func (h hostRequest) host() core.Host {
	present := make(map[string]bool, len(h.Paths)+len(h.Sockets))
	for _, value := range h.Paths {
		present[value] = true
	}
	sockets := make(map[string]bool, len(h.Sockets))
	for _, value := range h.Sockets {
		present[value] = true
		sockets[value] = true
	}
	return core.Host{
		UID:    h.UID,
		Home:   h.Home,
		Getenv: func(name string) string { return h.Env[name] },
		Glob: func(pattern string) ([]string, error) {
			matches := []string{}
			for value := range present {
				if ok, err := path.Match(pattern, value); err == nil && ok {
					matches = append(matches, value)
				}
			}
			sort.Strings(matches)
			return matches, nil
		},
		Exists:   func(value string) bool { return present[value] },
		IsSocket: func(value string) bool { return sockets[value] },
		ReadFile: func(value string) ([]byte, error) {
			contents, ok := h.Files[value]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(contents), nil
		},
	}
}

// manifestSource carries a manifest either as the object it is or as the text
// somebody typed. The text form exists because a manifest that does not parse
// is the most useful one to be told about, and it cannot be nested inside a
// request as an object.
type manifestSource struct {
	Manifest     json.RawMessage `json:"manifest,omitempty"`
	ManifestText string          `json:"manifestText,omitempty"`
}

func (m manifestSource) bytes() ([]byte, error) {
	if strings.TrimSpace(m.ManifestText) != "" {
		return []byte(m.ManifestText), nil
	}
	if len(m.Manifest) == 0 {
		return nil, errors.New("no manifest was given")
	}
	return m.Manifest, nil
}

func (m manifestSource) decode() (*types.CpakManifest, error) {
	raw, err := m.bytes()
	if err != nil {
		return nil, err
	}
	return core.DecodeManifest(raw)
}

func decodeRequest(payload string, value any) error {
	if strings.TrimSpace(payload) == "" {
		payload = "{}"
	}
	if err := json.Unmarshal([]byte(payload), value); err != nil {
		return errors.New("the request is not a JSON object: " + err.Error())
	}
	return nil
}
