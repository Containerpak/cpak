/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5/introspect"
	"golang.org/x/sys/unix"

	"github.com/mirkobrombin/cpak/pkg/integrity"
)

func testAnchorLedger(t *testing.T) AnchorLedger {
	t.Helper()
	return AnchorLedger{
		Directory: filepath.Join(t.TempDir(), "integrity", "v1"),
		OwnerUID:  uint32(os.Getuid()),
	}
}

func testAnchor() integrity.Anchor {
	packageRoot := strings.Repeat("a1", 32)
	policyRoot := strings.Repeat("b2", 32)
	return integrity.Anchor{
		ABI:         integrity.ABIVersion,
		UID:         uint32(os.Getuid()),
		Origin:      "github.com/singularityos-lab/singularity-desktop",
		Generation:  7,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  integrity.LaunchRoot(packageRoot, policyRoot),
	}
}

func writeAnchorFile(t *testing.T, ledger AnchorLedger, uid uint32, origin string, data []byte) string {
	t.Helper()
	path, err := ledger.anchorPath(uid, origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(filepath.Dir(path), ledger.OwnerUID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnchorLedgerRoundTripsAnEnrolment(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("an empty ledger reported %v, %v", found, err)
	}
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the enrolment was not recorded: %v, %v", found, err)
	}
	if loaded != anchor {
		t.Fatalf("got %+v, want %+v", loaded, anchor)
	}
	if _, found, err := ledger.Load(anchor.UID+1, anchor.Origin); err != nil || found {
		t.Fatalf("an anchor answered for another user: %v, %v", found, err)
	}
	if _, found, err := ledger.Load(anchor.UID, "github.com/example/other"); err != nil || found {
		t.Fatalf("an anchor answered for another origin: %v, %v", found, err)
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err != nil {
		t.Fatalf("forgetting an anchor that is not recorded failed: %v", err)
	}
}

func TestAnchorLedgerRefusesAGenerationDowngrade(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	older := anchor
	older.Generation = anchor.Generation - 1
	err := ledger.Store(older)
	if !errors.Is(err, ErrAnchorDowngrade) {
		t.Fatalf("got %v, want a downgrade refusal", err)
	}
	recorded, _, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Generation != anchor.Generation {
		t.Fatalf("the refused enrolment changed the recorded generation to %d", recorded.Generation)
	}
	if err := ledger.Store(anchor); err != nil {
		t.Fatalf("re-enrolling the recorded generation failed: %v", err)
	}
	newer := anchor
	newer.Generation = anchor.Generation + 1
	if err := ledger.Store(newer); err != nil {
		t.Fatalf("a newer generation was refused: %v", err)
	}
}

func TestAnchorLedgerRejectsAnAnchorAnybodyCanWrite(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	path, err := ledger.anchorPath(anchor.UID, anchor.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("a world writable anchor was trusted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor in a world writable directory was trusted")
	}
	if err := ledger.Forget(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor was unlinked from a world writable directory")
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ledger.Directory, 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor under a world writable ledger root was trusted")
	}
}

func TestAnchorLedgerRejectsAnUnexpectedOwner(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	if err := ledger.Store(anchor); err != nil {
		t.Fatal(err)
	}
	foreign := ledger
	foreign.OwnerUID = ledger.OwnerUID + 1
	if _, _, err := foreign.Load(anchor.UID, anchor.Origin); err == nil {
		t.Fatal("an anchor written by another user was trusted")
	}
}

func TestAnchorLedgerRejectsAMalformedRecord(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := append(bytes.TrimSuffix(encoded, []byte("}")), []byte(`,"trusted":true}`)...)
	for name, data := range map[string][]byte{
		"an unknown field":      unknownField,
		"two JSON values":       append(append([]byte{}, encoded...), encoded...),
		"a record over the cap": append(append([]byte{}, encoded...), bytes.Repeat([]byte(" "), anchorSizeLimit)...),
		"no JSON at all":        []byte("not an anchor"),
	} {
		writeAnchorFile(t, ledger, anchor.UID, anchor.Origin, data)
		if _, _, err := ledger.Load(anchor.UID, anchor.Origin); err == nil {
			t.Fatalf("%s was accepted as an anchor", name)
		}
	}
}

func TestAnchorLedgerRejectsARecordThatDoesNotMatchItsFile(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	encoded, err := json.Marshal(anchor)
	if err != nil {
		t.Fatal(err)
	}
	other := "github.com/example/other"
	writeAnchorFile(t, ledger, anchor.UID, other, encoded)
	if _, _, err := ledger.Load(anchor.UID, other); err == nil {
		t.Fatal("an anchor was read from the file of another origin")
	}
	writeAnchorFile(t, ledger, anchor.UID+1, anchor.Origin, encoded)
	if _, _, err := ledger.Load(anchor.UID+1, anchor.Origin); err == nil {
		t.Fatal("an anchor was read from the file of another user")
	}
}

func TestAnchorLedgerKeepsAnOriginInsideTheLedger(t *testing.T) {
	root := t.TempDir()
	ledger := AnchorLedger{Directory: filepath.Join(root, "integrity", "v1"), OwnerUID: uint32(os.Getuid())}
	anchor := testAnchor()
	for _, origin := range []string{
		"../../etc/passwd",
		"github.com/../../etc",
		"github.com/owner/../../../escaped",
		"github.com/owner/..",
		"/etc/passwd",
		"github.com/owner",
	} {
		escaping := anchor
		escaping.Origin = origin
		if err := ledger.Store(escaping); err == nil {
			t.Fatalf("%q was accepted as an origin", origin)
		}
		if _, _, err := ledger.Load(escaping.UID, origin); err == nil {
			t.Fatalf("%q was read as an origin", origin)
		}
		if err := ledger.Forget(escaping.UID, origin); err == nil {
			t.Fatalf("%q was removed as an origin", origin)
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return errors.New("a refused origin wrote " + path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnchorFileIsUniqueForEveryOrigin(t *testing.T) {
	ledger := testAnchorLedger(t)
	first := testAnchor()
	first.Origin = "github.com/team_one/desktop"
	second := testAnchor()
	second.Origin = "github.com/team/one_desktop"
	second.Generation = first.Generation + 3
	for _, anchor := range []integrity.Anchor{first, second} {
		if err := ledger.Store(anchor); err != nil {
			t.Fatal(err)
		}
	}
	for _, anchor := range []integrity.Anchor{first, second} {
		loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
		if err != nil || !found {
			t.Fatalf("%s lost its anchor: %v, %v", anchor.Origin, found, err)
		}
		if loaded != anchor {
			t.Fatalf("%s reads back as %+v", anchor.Origin, loaded)
		}
	}
}

func TestAnchorLedgerRejectsAnUnusableAnchor(t *testing.T) {
	ledger := testAnchorLedger(t)
	otherABI := testAnchor()
	otherABI.ABI = integrity.ABIVersion + 1
	forgedLaunchRoot := testAnchor()
	forgedLaunchRoot.LaunchRoot = strings.Repeat("c3", 32)
	shortRoot := testAnchor()
	shortRoot.PackageRoot = "a1b2c3"
	uppercaseRoot := testAnchor()
	uppercaseRoot.PolicyRoot = strings.ToUpper(uppercaseRoot.PolicyRoot)
	for name, anchor := range map[string]integrity.Anchor{
		"another abi":                  otherABI,
		"a launch root of its own":     forgedLaunchRoot,
		"a root that is not a digest":  shortRoot,
		"a root that is not lowercase": uppercaseRoot,
	} {
		if err := ledger.Store(anchor); err == nil {
			t.Fatalf("an anchor with %s was enrolled", name)
		}
	}
}

func TestAuthoritySocketEnrollsAnAnchorWithoutABus(t *testing.T) {
	ledger := testAnchorLedger(t)
	path := startAuthoritySocket(t, socketService{
		Anchors:   ledger,
		Authorize: func(*unix.Ucred) error { return nil },
	})
	anchor := testAnchor()
	request := socketRequest{Action: anchorEnrollAction, Anchor: &anchor}
	if err := requestOverSocket(path, request); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := ledger.Load(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the anchor was not enrolled: %v, %v", found, err)
	}
	if loaded != anchor {
		t.Fatalf("got %+v, want %+v", loaded, anchor)
	}
	older := anchor
	older.Generation = anchor.Generation - 1
	request.Anchor = &older
	err = asDowngrade(requestOverSocket(path, request))
	if !errors.Is(err, ErrAnchorDowngrade) {
		t.Fatalf("got %v, want a downgrade the caller can recognise", err)
	}
	if err := requestOverSocket(path, socketRequest{Action: anchorForgetAction, Origin: anchor.Origin, UID: anchor.UID}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
}

func TestAuthoritySocketRefusesAnAnchorFromAnUnauthorizedPeer(t *testing.T) {
	ledger := testAnchorLedger(t)
	path := startAuthoritySocket(t, socketService{
		Anchors:   ledger,
		Authorize: func(*unix.Ucred) error { return errors.New("not allowed here") },
	})
	anchor := testAnchor()
	err := requestOverSocket(path, socketRequest{
		Action: anchorEnrollAction, Anchor: &anchor,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed here") {
		t.Fatalf("the refusal did not reach the caller: %v", err)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

func TestAuthoritySocketRejectsAnEnrolmentWithoutAnAnchor(t *testing.T) {
	path := startAuthoritySocket(t, socketService{
		Anchors:   testAnchorLedger(t),
		Authorize: func(*unix.Ucred) error { return nil },
	})
	if err := requestOverSocket(path, socketRequest{Action: anchorEnrollAction, Origin: testAnchor().Origin}); err == nil {
		t.Fatal("an enrolment without an anchor was accepted")
	}
}

func TestServiceAuthorizesEveryAnchorMutation(t *testing.T) {
	ledger := testAnchorLedger(t)
	authorizer := &testAuthorizer{}
	service := Service{Anchors: ledger, Authorizer: authorizer}
	anchor := testAnchor()
	dbusErr := service.EnrollAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot)
	if dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionEnrollAnchor || authorizer.details["package-origin"] != anchor.Origin {
		t.Fatalf("unexpected enrolment policy request: %s %#v", authorizer.action, authorizer.details)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || !found {
		t.Fatalf("the authorized enrolment was not recorded: %v, %v", found, err)
	}
	if dbusErr := service.ForgetAnchor(":1.20", anchor.UID, anchor.Origin); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionForgetAnchor {
		t.Fatalf("unexpected removal policy request: %s", authorizer.action)
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the anchor survived its removal: %v, %v", found, err)
	}
}

func TestServiceRejectsAnInvalidAnchorBeforeAuthorization(t *testing.T) {
	authorizer := &testAuthorizer{}
	service := Service{Anchors: testAnchorLedger(t), Authorizer: authorizer}
	anchor := testAnchor()
	dbusErr := service.EnrollAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, strings.Repeat("c3", 32))
	if dbusErr == nil {
		t.Fatal("an anchor with a launch root of its own was accepted")
	}
	if authorizer.action != "" {
		t.Fatal("invalid request reached the authorization service")
	}
}

func TestServiceDenialDoesNotEnrollAnAnchor(t *testing.T) {
	ledger := testAnchorLedger(t)
	service := Service{Anchors: ledger, Authorizer: &testAuthorizer{err: errors.New("denied")}}
	anchor := testAnchor()
	dbusErr := service.EnrollAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot)
	if dbusErr == nil {
		t.Fatal("authorization denial was ignored")
	}
	if _, found, err := ledger.Load(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("denied enrolment changed the ledger: %v, %v", found, err)
	}
}

// The bus carries plain values, so a method it cannot describe is a method no
// caller can reach. This pins both that the anchor methods are describable and
// the order their arguments travel in.
func TestAnchorMethodsAreCarriedByTheBusInterface(t *testing.T) {
	arguments := map[string]string{}
	for _, method := range introspect.Methods(&Service{}) {
		signature := ""
		for _, argument := range method.Args {
			if argument.Direction == "in" {
				signature += argument.Type
			}
		}
		arguments[method.Name] = signature
	}
	for name, want := range map[string]string{"EnrollAnchor": "iustsss", "ForgetAnchor": "us"} {
		if arguments[name] != want {
			t.Fatalf("%s takes %q on the bus, want %q", name, arguments[name], want)
		}
	}
}

func TestAnchorActionsAreDeclaredInThePolkitPolicy(t *testing.T) {
	for _, action := range []string{ActionEnrollAnchor, ActionForgetAnchor} {
		declaration := `<action id="` + action + `">`
		if !strings.Contains(string(polkitPolicy), declaration) {
			t.Fatalf("%s is not declared in the polkit policy", action)
		}
	}
}
