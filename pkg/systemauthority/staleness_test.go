/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// Upgrading cpak leaves the authority that was already running, and it is the
// binary the upgrade replaced. Nobody at the keyboard can be expected to know
// that, so the service notices and the caller asks again.
func TestAStaleAuthorityStepsAsideInsteadOfAnswering(t *testing.T) {
	stepped := 0
	saved := stepAside
	t.Cleanup(func() { stepAside = saved })
	stepAside = func() { stepped++ }

	refusal := refuseIfStale()
	if runningIsInstalled() {
		if refusal != nil {
			t.Fatalf("a current authority refused: %v", refusal)
		}
		if stepped != 0 {
			t.Fatal("a current authority stepped aside")
		}
		return
	}
	if refusal == nil {
		t.Fatal("a stale authority answered the request")
	}
	if refusal.Name != errAuthorityStaleName {
		t.Fatalf("the refusal is named %q, which no caller retries on", refusal.Name)
	}
	if stepped != 1 {
		t.Fatalf("the stale authority stayed up: stepped %d times", stepped)
	}
}

// A host with nothing installed has no newer authority to make way for, and
// refusing every request there would break the case the guard is not for.
func TestAnAuthorityOnAHostWithNoInstallationServes(t *testing.T) {
	if _, found := installedLayout(); found {
		t.Skip("this host has an installation")
	}
	if !runningIsInstalled() {
		t.Fatal("a host with nothing installed reported a stale authority")
	}
}

// The retry is the whole of what makes the upgrade invisible, and it happens
// once: a second refusal is an answer.
func TestTheCallerAsksAgainExactlyOncePastAStaleAuthority(t *testing.T) {
	attempts := 0
	err := retryPastStale(func() error {
		attempts++
		if attempts == 1 {
			return dbus.Error{Name: errAuthorityStaleName, Body: []any{"stale"}}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the second attempt failed: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("the call was made %d times, want one retry", attempts)
	}

	attempts = 0
	err = retryPastStale(func() error {
		attempts++
		return dbus.Error{Name: errAuthorityStaleName, Body: []any{"stale"}}
	})
	if err == nil {
		t.Fatal("an authority that is stale twice was reported as success")
	}
	if attempts != 2 {
		t.Fatalf("the call was made %d times, want it to give up after one retry", attempts)
	}
}

// Everything that is not that refusal is an answer, and repeating it would
// ask a user to be authorised twice or record something twice.
func TestNothingElseIsRetried(t *testing.T) {
	for name, answer := range map[string]error{
		"a refusal":     dbus.Error{Name: "it.cpak.Error.NotAuthorized", Body: []any{"no"}},
		"a plain error": errors.New("the ledger could not be written"),
		"success":       nil,
	} {
		attempts := 0
		_ = retryPastStale(func() error {
			attempts++
			return answer
		})
		if attempts != 1 {
			t.Fatalf("%s was attempted %d times", name, attempts)
		}
	}
}

// Every method the bus serves has to pass the guard, or an upgrade heals on
// some calls and not on others, which is worse than not healing at all.
func TestEveryServedMethodChecksForStaleness(t *testing.T) {
	guarded := map[string]bool{
		"RegisterSession": true, "RemoveSession": true, "EnrolAnchor": true,
		"EnrolSignedAnchor": true, "SetEnforcement": true, "ForgetAnchor": true,
		"SetTrustPolicy": true, "SetSignaturePolicy": true,
	}
	for _, method := range introspect.Methods(&Service{}) {
		if !guarded[method.Name] {
			t.Fatalf("%s is served on the bus and is not in the guarded set", method.Name)
		}
	}
}

// The comparison is the whole guard, so it is exercised against real files
// rather than trusted to read correctly. An installed binary that is not this
// process is the case an upgrade produces.
func TestTheGuardComparesTheFileAndNotThePath(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("a trusted installed binary has to be owned by root")
	}
	prefix := t.TempDir()
	saved := installPrefixes
	t.Cleanup(func() { installPrefixes = saved })
	installPrefixes = []string{prefix}

	binary := layoutFor(prefix).binary
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}

	// Nothing installed: there is no newer authority to make way for.
	if !runningIsInstalled() {
		t.Fatal("a host with no installed binary reported a stale authority")
	}

	// A different file at the installed path is exactly what an upgrade leaves
	// behind, and the path is unchanged throughout.
	if err := os.WriteFile(binary, []byte("another cpak"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runningIsInstalled() {
		t.Fatal("an authority running a binary the host has replaced reported itself current")
	}

	// This very process at the installed path is the ordinary case, and it must
	// not be read as stale or every request on a healthy host is refused.
	running, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Skip("this kernel does not let a process read its own image")
	}
	if err := os.WriteFile(binary, running, 0o755); err != nil {
		t.Fatal(err)
	}
	if runningIsInstalled() {
		t.Fatal("a copy of the same bytes was read as the same file, so a real replacement would be missed")
	}
	// A hard link is the same file, and that is what the guard is asking about.
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	// The magic link itself cannot be linked, so the file it names is.
	image, err := os.Readlink("/proc/self/exe")
	if err != nil {
		t.Skip("this kernel does not name the running image")
	}
	if err := os.Link(image, binary); err != nil {
		t.Skipf("this filesystem will not link the running image: %v", err)
	}
	if !runningIsInstalled() {
		t.Fatal("the installed binary is this process and the guard said otherwise")
	}
}
