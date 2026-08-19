/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// A container asking to run one of its declared dependencies used to say which
// application it was, and the service believed it.
//
// The value it sent was not a secret. An application's identifier is
// base64(name:sourceType:version:origin), all of it public metadata, so it can
// be computed offline for any installed package, and the socket is mounted into
// every container. A package holding no permissions of its own could therefore
// name a widely permissioned application as its parent and run one of that
// application's dependencies under the victim's policy.
//
// Now the marker holds a secret instead of a name. cpak writes it when it builds
// the container and records it beside the container, so presenting it proves
// which container is calling rather than asserting it. The identity is resolved
// from the token, never read off the wire.

// nestedTokenBytes is the size of the secret. It matches the system broker
// token, which solves the same problem one file away.
const nestedTokenBytes = 32

// newNestedToken answers a fresh capability for one container.
func newNestedToken() (string, error) {
	raw := make([]byte, nestedTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate the nested run token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// validNestedToken reports whether a value has the shape of a token, so a
// malformed one is refused before anything is compared against it.
func validNestedToken(token string) bool {
	if len(token) != nestedTokenBytes*2 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

// errNestedUnknownCaller is what a caller is told when its token names no
// container. It says nothing about which part was wrong, because a caller that
// could tell a malformed token from an unknown one could search for a real one.
var errNestedUnknownCaller = errors.New("the nested request does not come from a container this cpak built")

// parentForNestedToken answers which application the caller is, by finding the
// container that holds the token it presented.
//
// The comparison is constant time because this is a capability check, and a
// caller must not learn a token by measuring how long a wrong one takes to be
// refused. The search is over installed applications rather than a direct
// lookup because the store indexes containers by their application, which is
// the shape it already had.
func parentForNestedToken(store *Store, token string) (types.Application, error) {
	if !validNestedToken(token) {
		return types.Application{}, errNestedUnknownCaller
	}
	applications, err := store.GetApplications()
	if err != nil {
		return types.Application{}, err
	}
	presented := []byte(token)
	for _, application := range applications {
		containers, err := store.GetApplicationContainers(application)
		if err != nil {
			return types.Application{}, err
		}
		for _, container := range containers {
			if container.NestedToken == "" {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(container.NestedToken), presented) == 1 {
				return application, nil
			}
		}
	}
	return types.Application{}, errNestedUnknownCaller
}

// readNestedToken answers the token this container was given, from inside it.
func readNestedToken() (string, bool) {
	content, err := os.ReadFile(nestedMarkerPath)
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(string(content))
	if token == "" {
		return "", false
	}
	return token, true
}
