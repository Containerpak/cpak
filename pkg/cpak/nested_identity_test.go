/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// The attack the capability exists to stop. An application identifier is
// base64(name:sourceType:version:origin), all of it public, so a package with no
// permissions could compute the identifier of a widely permissioned one, name it
// as its parent, and be believed.
func TestAnIdentifierAnybodyCanComputeIsNotAnIdentity(t *testing.T) {
	// What an attacker can work out offline, without ever seeing the victim.
	guessed := base64.StdEncoding.EncodeToString([]byte("demo:github:1.0:github.com/containerpak/demo"))
	if validNestedToken(guessed) {
		t.Fatal("a computed application identifier was accepted as a capability")
	}
	for _, guess := range []string{
		"", "demo", strings.Repeat("0", 63), strings.Repeat("0", 65),
		strings.Repeat("g", 64), "github.com/containerpak/demo",
	} {
		if validNestedToken(guess) {
			t.Fatalf("%q was accepted as a capability", guess)
		}
	}
}

func TestACapabilityIsUnguessableAndDifferentEveryTime(t *testing.T) {
	seen := map[string]bool{}
	for index := 0; index < 64; index++ {
		token, err := newNestedToken()
		if err != nil {
			t.Fatal(err)
		}
		if !validNestedToken(token) {
			t.Fatalf("a minted capability did not pass its own check: %q", token)
		}
		if seen[token] {
			t.Fatal("two containers were given the same capability")
		}
		seen[token] = true
	}
}

// A request naming no capability, or one no container holds, is refused, and the
// refusal is the same either way so a caller cannot search for a real one.
func TestARequestWithoutACapabilityIsRefused(t *testing.T) {
	for name, params := range map[string]types.RequestParams{
		"nothing at all":  {Action: "run", Origin: "github.com/containerpak/demo", Binary: "/usr/bin/demo"},
		"a plausible one": {Action: "run", Token: strings.Repeat("ab", 32), Origin: "github.com/containerpak/demo", Binary: "/usr/bin/demo"},
	} {
		err := validateNestedRequest(params)
		if name == "nothing at all" && err == nil {
			t.Fatal("a request carrying no capability was accepted")
		}
		if name == "a plausible one" && err != nil {
			t.Fatalf("a well formed capability was refused by shape: %v", err)
		}
	}
}

// The one a container really holds resolves; anything else does not, and the
// answer says nothing about which part was wrong.
func TestOnlyTheContainerThatHoldsItIsRecognised(t *testing.T) {
	cp := newTestCpak(t)
	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	app := types.Application{CpakId: "parent", Name: "demo", Version: "1", Origin: "github.com/containerpak/demo"}
	if err := store.NewApplication(app); err != nil {
		t.Fatal(err)
	}
	token, err := newNestedToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewContainer(types.Container{
		CpakId:            "container",
		ApplicationCpakId: app.CpakId,
		NestedToken:       token,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := parentForNestedToken(store, token)
	if err != nil {
		t.Fatalf("a container's own capability was not recognised: %v", err)
	}
	if resolved.CpakId != app.CpakId {
		t.Fatalf("the capability resolved to %q", resolved.CpakId)
	}

	other, err := newNestedToken()
	if err != nil {
		t.Fatal(err)
	}
	for name, presented := range map[string]string{
		"another container's": other,
		"the application id":  app.CpakId,
		"nothing":             "",
	} {
		if _, err := parentForNestedToken(store, presented); !errors.Is(err, errNestedUnknownCaller) {
			t.Fatalf("%s was answered with %v instead of an unknown caller", name, err)
		}
	}
}
