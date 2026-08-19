/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5/introspect"
	"golang.org/x/sys/unix"

	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/trustpolicy"
)

// The administrator's policy and the ledger it governs are the same directory,
// because a policy held anywhere the launching account can reach would be a
// policy that account can decide.
func testTrustedLedger(t *testing.T) (AnchorLedger, TrustStore) {
	t.Helper()
	ledger := testAnchorLedger(t)
	return ledger, TrustStore{Directory: ledger.Directory, OwnerUID: ledger.OwnerUID}
}

// approvedSigner is the identity every signature in this file is made by, in
// the shape an administrator writes it down.
func approvedSigner() trustpolicy.Signer {
	identity := testSignatureIdentity(testAnchor().Origin)
	return trustpolicy.Signer{Issuer: identity.Issuer, Repo: identity.Repo}
}

func setTrustPolicy(t *testing.T, store TrustStore, policy trustpolicy.Policy) {
	t.Helper()
	if err := store.Set(policy); err != nil {
		t.Fatalf("the administrator could not set a policy: %v", err)
	}
}

func writeTrustFile(t *testing.T, store TrustStore, data []byte) string {
	t.Helper()
	if err := ensureDirectory(store.Directory, store.OwnerUID); err != nil {
		t.Fatal(err)
	}
	path, err := store.path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// useApprovalVerifier drives the counter-signature check the authority acts on
// and puts back whatever the build ships with when the test ends.
func useApprovalVerifier(t *testing.T, verify func(Enrolment) (signature.Verified, error)) {
	t.Helper()

	saved := verifyApproval
	t.Cleanup(func() { verifyApproval = saved })
	verifyApproval = verify
}

// The one test the whole file is built around. Every installation that exists
// today has no policy, so a host that was never managed must record exactly
// what it records now, signed or not, and must not even gain a file it did not
// ask for.
func TestAnUnmanagedHostRecordsEnrolmentsExactlyAsItDid(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	policy, err := store.Policy()
	if err != nil {
		t.Fatalf("a host that decided nothing answered %v", err)
	}
	if !policy.Empty() {
		t.Fatalf("a host that decided nothing holds the policy %+v", policy)
	}
	unsigned := testAnchor()
	unsigned.Origin = "github.com/acme/nobody-signed-this"
	if err := ledger.Record(Enrolment{Anchor: unsigned}); err != nil {
		t.Fatalf("an unmanaged host refused an unsigned enrolment: %v", err)
	}
	anchor := testAnchor()
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(3)}); err != nil {
		t.Fatalf("an unmanaged host refused a signed enrolment: %v", err)
	}
	for _, origin := range []string{unsigned.Origin, anchor.Origin} {
		if _, found, err := ledger.Recorded(anchor.UID, origin); err != nil || !found {
			t.Fatalf("%s was not recorded on an unmanaged host: %v, %v", origin, found, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Directory, trustPolicyFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unmanaged host gained a trust policy of its own: %v", err)
	}
}

// A policy that states an ABI and decides nothing else is still a host nobody
// manages, so it has to behave as one.
func TestAPolicyThatDecidesNothingAllowsEverything(t *testing.T) {
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{ABI: 1})
	anchor := testAnchor()
	if err := ledger.Record(Enrolment{Anchor: anchor}); err != nil {
		t.Fatalf("a policy that decides nothing refused an enrolment: %v", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || !found {
		t.Fatalf("the enrolment was not recorded: %v, %v", found, err)
	}
}

func TestTheTrustPolicyRoundTripsWhatTheAdministratorSet(t *testing.T) {
	_, store := testTrustedLedger(t)
	want := trustpolicy.Policy{
		ABI:              1,
		RequirePublisher: true,
		ApprovedSigners:  []trustpolicy.Signer{approvedSigner()},
		ApprovedOrigins:  []string{testAnchor().Origin},
		Revoked:          []trustpolicy.Revocation{{Origin: testAnchor().Origin, Generation: 2, Reason: "withdrawn"}},
	}
	setTrustPolicy(t, store, want)
	got, err := store.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if got.Empty() {
		t.Fatal("a policy the administrator set reads back as no policy at all")
	}
	first, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("the policy reads back as %s, want %s", second, first)
	}
}

// The policy decides who may publish for every account on the host, so a file
// the launching account could have written, or one this build cannot fully
// understand, must never be read as that decision.
func TestTheTrustPolicyRejectsWhatItCannotTrust(t *testing.T) {
	_, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{ABI: 1, ApprovedOrigins: []string{testAnchor().Origin}})
	path := filepath.Join(store.Directory, trustPolicyFileName)
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if policy, err := store.Policy(); err == nil || !policy.Empty() {
		t.Fatalf("a world writable policy was trusted: %+v, %v", policy, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Directory, 0777); err != nil {
		t.Fatal(err)
	}
	if policy, err := store.Policy(); err == nil || !policy.Empty() {
		t.Fatalf("a policy in a world writable directory was trusted: %+v, %v", policy, err)
	}
	if err := os.Chmod(store.Directory, 0755); err != nil {
		t.Fatal(err)
	}
	foreign := store
	foreign.OwnerUID = store.OwnerUID + 1
	if policy, err := foreign.Policy(); err == nil || !policy.Empty() {
		t.Fatalf("a policy written by another account was trusted: %+v, %v", policy, err)
	}
	for name, document := range map[string]string{
		"an unknown field":     `{"abi":1,"require_everything":true}`,
		"two JSON values":      `{"abi":1}{"abi":1}`,
		"an unreadable abi":    `{"abi":98765}`,
		"something not a poli": `not a policy at all`,
	} {
		writeTrustFile(t, store, []byte(document))
		if policy, err := store.Policy(); err == nil || !policy.Empty() {
			t.Fatalf("a policy holding %s was trusted: %+v, %v", name, policy, err)
		}
	}
	writeTrustFile(t, store, []byte(strings.Repeat("x", trustPolicySizeLimit+1)))
	if policy, err := store.Policy(); err == nil || !policy.Empty() {
		t.Fatalf("a policy nobody bounded was trusted: %+v, %v", policy, err)
	}
}

// Reading a broken policy as no policy would let whoever broke it decide that
// this host is unmanaged. An enrolment can be refused without taking anything
// away from a user, so the unreadable case fails on the strict side.
func TestAPolicyThatCannotBeTrustedRefusesTheEnrolment(t *testing.T) {
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{ABI: 1, ApprovedOrigins: []string{testAnchor().Origin}})
	if err := os.Chmod(filepath.Join(store.Directory, trustPolicyFileName), 0666); err != nil {
		t.Fatal(err)
	}
	anchor := testAnchor()
	if err := ledger.Record(Enrolment{Anchor: anchor}); err == nil {
		t.Fatal("an enrolment was recorded against a policy nobody can trust")
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

// A managed machine must not be one install away from arbitrary software, and
// the origin list is the only thing that says so: the publisher signature never
// could, because every package is signed by the origin it names itself.
func TestAnOriginTheAdministratorHasNotApprovedIsNeverEnrolled(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		ApprovedOrigins: []string{"github.com/acme/approved-desktop"},
	})
	anchor := testAnchor()
	err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)})
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("an unapproved origin was refused with %v, want a trust refusal", err)
	}
	if !strings.Contains(err.Error(), anchor.Origin) {
		t.Fatalf("the refusal does not say which origin it refused: %v", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
	setTrustPolicy(t, store, trustpolicy.Policy{ABI: 1, ApprovedOrigins: []string{anchor.Origin}})
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatalf("an approved origin was refused: %v", err)
	}
}

// The identity that signed is put to the list the administrator wrote, and not
// to the origin the package names itself. This is the whole point of the
// round: without it the signature check answers a question the package asked.
func TestAPublisherTheAdministratorHasNotApprovedIsNeverEnrolled(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:              1,
		RequirePublisher: true,
		ApprovedSigners:  []trustpolicy.Signer{{Issuer: approvedSigner().Issuer, Repo: "github.com/acme/release-engineering"}},
	})
	anchor := testAnchor()
	err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)})
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("an unapproved publisher was refused with %v, want a trust refusal", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
	// The same enrolment, once the administrator approves who signed it, and
	// with no origin list at all: a policy that decides one thing must not be
	// read as deciding the others.
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:              1,
		RequirePublisher: true,
		ApprovedSigners:  []trustpolicy.Signer{approvedSigner()},
	})
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatalf("an approved publisher was refused: %v", err)
	}
}

func TestAHostThatRequiresAPublisherRefusesAPackageNobodySigned(t *testing.T) {
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:              1,
		RequirePublisher: true,
		ApprovedSigners:  []trustpolicy.Signer{approvedSigner()},
	})
	anchor := testAnchor()
	err := ledger.Record(Enrolment{Anchor: anchor})
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("an unsigned package was refused with %v, want a trust refusal", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

// Approval that cannot be withdrawn is not a control. A revocation names a
// publisher release, because that is the number that means the same thing on
// every machine in a fleet.
func TestARevokedReleaseIsNeverEnrolled(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	anchor := testAnchor()
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		ApprovedOrigins: []string{anchor.Origin},
		Revoked:         []trustpolicy.Revocation{{Origin: anchor.Origin, Generation: 4, Reason: "it shipped a stealer"}},
	})
	err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(4)})
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("a revoked release was refused with %v, want a trust refusal", err)
	}
	if !strings.Contains(err.Error(), "it shipped a stealer") {
		t.Fatalf("the refusal does not carry the administrator's reason: %v", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the revoked release reached the ledger: %v, %v", found, err)
	}
	// The release the publisher put out next is a different state, and nothing
	// was withdrawn from it.
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(5)}); err != nil {
		t.Fatalf("the release after the revoked one was refused: %v", err)
	}
}

// A revocation with no generation withdraws the whole origin, which is what an
// administrator reaches for when a publisher is compromised rather than one
// release of it. It has to reach an unsigned enrolment as well, because an
// unsigned package names no release to withdraw.
func TestARevocationWithNoGenerationWithdrawsTheWholeOrigin(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	anchor := testAnchor()
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		ApprovedOrigins: []string{anchor.Origin},
		Revoked:         []trustpolicy.Revocation{{Origin: anchor.Origin}},
	})
	for name, enrolment := range map[string]Enrolment{
		"a signed release": {Anchor: anchor, Signature: testSignedState(9)},
		"an unsigned one":  {Anchor: anchor},
	} {
		if err := ledger.Record(enrolment); !errors.Is(err, ErrTrustRefused) {
			t.Fatalf("%s of a revoked origin was refused with %v, want a trust refusal", name, err)
		}
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("a revoked origin reached the ledger: %v, %v", found, err)
	}
}

// The counter-signature is the second party: the organisation saying it looked
// at this exact state, rather than the publisher saying so about itself.
func TestAnApprovalIsCheckedAgainstWhoTheAdministratorLetsApprove(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	anchor := testAnchor()
	approver := trustpolicy.Signer{Issuer: approvedSigner().Issuer, Repo: "github.com/acme/security"}
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		RequireApproval: true,
		ApprovalSigners: []trustpolicy.Signer{approver},
	})
	approvalBy := func(repo string) func(Enrolment) (signature.Verified, error) {
		return func(enrolment Enrolment) (signature.Verified, error) {
			return signature.Verified{
				State:    enrolment.Signature.State,
				Identity: testSignatureIdentity(repo),
			}, nil
		}
	}
	useApprovalVerifier(t, approvalBy("github.com/acme/somebody-else"))
	err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)})
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("an approval by nobody the administrator named was refused with %v, want a trust refusal", err)
	}
	useApprovalVerifier(t, func(Enrolment) (signature.Verified, error) {
		return signature.Verified{}, errors.New("the counter-signature does not cover this state")
	})
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("an approval that does not stand was refused with %v, want a trust refusal", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("an unapproved state reached the ledger: %v, %v", found, err)
	}
	useApprovalVerifier(t, approvalBy(approver.Repo))
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); err != nil {
		t.Fatalf("a state the administrator approved was refused: %v", err)
	}
}

// Answering that an approval holds because nothing looked for one is the
// failure this round exists to remove, so a build with no counter-signature
// check refuses instead of passing.
func TestAHostThatRequiresAnApprovalRefusesWhenNothingCanCheckOne(t *testing.T) {
	acceptSignaturesOf(t, testAnchor().Origin)
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		RequireApproval: true,
		ApprovalSigners: []trustpolicy.Signer{{Issuer: approvedSigner().Issuer, Repo: "github.com/acme/security"}},
	})
	useApprovalVerifier(t, nil)
	anchor := testAnchor()
	if err := ledger.Record(Enrolment{Anchor: anchor, Signature: testSignedState(1)}); !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("a host with nothing to check an approval recorded anyway, %v", err)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("an unchecked approval reached the ledger: %v, %v", found, err)
	}
}

// The policy is enforced where the answer is written down, so no transport and
// no client can be the thing that skipped it. The socket carries the requests
// of a host with no bus, and the bus method is the one an unprivileged install
// reaches.
func TestTheTrustPolicyIsEnforcedOnEveryTransport(t *testing.T) {
	ledger, store := testTrustedLedger(t)
	setTrustPolicy(t, store, trustpolicy.Policy{
		ABI:             1,
		ApprovedOrigins: []string{"github.com/acme/approved-desktop"},
	})
	anchor := testAnchor()
	path := startAuthoritySocket(t, socketService{
		Anchors:   ledger,
		Authorize: func(*unix.Ucred) error { return nil },
	})
	err := asRefusal(requestOverSocket(path, socketRequest{Action: anchorEnrolAction, Anchor: &anchor}))
	if !errors.Is(err, ErrTrustRefused) {
		t.Fatalf("the socket recorded an unapproved origin, %v", err)
	}
	// The authority is asked before the ledger refuses, so this pins the one
	// thing that matters: an authorization the administrator's policy does not
	// allow still records nothing.
	service := testAnchorService(ledger, &testAuthorizer{})
	dbusErr := service.EnrolAnchor(":1.20", int32(anchor.ABI), anchor.UID, anchor.Origin,
		anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, "")
	if dbusErr == nil {
		t.Fatal("the bus recorded an unapproved origin")
	}
	if !strings.Contains(dbusErr.Error(), ErrTrustRefused.Error()) {
		t.Fatalf("the bus refusal does not name the trust policy: %v", dbusErr)
	}
	if _, found, err := ledger.Recorded(anchor.UID, anchor.Origin); err != nil || found {
		t.Fatalf("the refused enrolment reached the ledger: %v, %v", found, err)
	}
}

func TestServiceAuthorizesEveryTrustPolicyChange(t *testing.T) {
	_, store := testTrustedLedger(t)
	authorizer := &testAuthorizer{}
	service := Service{Trust: store, Authorizer: authorizer}
	document, err := json.Marshal(trustpolicy.Policy{
		ABI:             1,
		ApprovedOrigins: []string{testAnchor().Origin},
		ApprovedSigners: []trustpolicy.Signer{approvedSigner()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dbusErr := service.SetTrustPolicy(":1.20", string(document)); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if authorizer.action != ActionSetTrustPolicy {
		t.Fatalf("changing the trust policy asked for %s", authorizer.action)
	}
	if authorizer.details["approved-origins"] != "1" || authorizer.details["approved-signers"] != "1" {
		t.Fatalf("the owner was asked about %#v", authorizer.details)
	}
	policy, err := store.Policy()
	if err != nil || policy.Empty() {
		t.Fatalf("the authorized policy reads back as %+v, %v", policy, err)
	}
}

func TestServiceDenialDoesNotChangeTheTrustPolicy(t *testing.T) {
	_, store := testTrustedLedger(t)
	service := Service{Trust: store, Authorizer: &testAuthorizer{err: errors.New("denied")}}
	if dbusErr := service.SetTrustPolicy(":1.20", `{"abi":1,"approved_origins":["github.com/acme/desktop"]}`); dbusErr == nil {
		t.Fatal("authorization denial was ignored")
	}
	policy, err := store.Policy()
	if err != nil || !policy.Empty() {
		t.Fatalf("a denied change left the policy at %+v, %v", policy, err)
	}
}

func TestServiceRejectsATrustPolicyItCannotReadBeforeAuthorization(t *testing.T) {
	_, store := testTrustedLedger(t)
	for _, document := range []string{
		`{"abi":1,"require_everything":true}`,
		`{"abi":98765}`,
		`{"abi":1}{"abi":1}`,
		`not a policy at all`,
	} {
		authorizer := &testAuthorizer{}
		service := Service{Trust: store, Authorizer: authorizer}
		if dbusErr := service.SetTrustPolicy(":1.20", document); dbusErr == nil {
			t.Fatalf("the authority accepted %s", document)
		}
		if authorizer.action != "" {
			t.Fatalf("%s reached the authorization service", document)
		}
	}
}

func TestTheTrustPolicyActionAsksTheOwnerOfTheMachine(t *testing.T) {
	defaults := policyDefaults(t)
	want := [3]string{"no", "no", "auth_admin"}
	if defaults[ActionSetTrustPolicy] != want {
		t.Fatalf("%s is declared as %v, want %v", ActionSetTrustPolicy, defaults[ActionSetTrustPolicy], want)
	}
}

func TestTheTrustPolicyMethodIsCarriedByTheBusInterface(t *testing.T) {
	for _, method := range introspect.Methods(&Service{}) {
		if method.Name != "SetTrustPolicy" {
			continue
		}
		taken := ""
		for _, argument := range method.Args {
			if argument.Direction == "in" {
				taken += argument.Type
			}
		}
		if taken != "s" {
			t.Fatalf("SetTrustPolicy takes %q on the bus, want %q", taken, "s")
		}
		return
	}
	t.Fatal("SetTrustPolicy is not a method the bus can describe")
}

// A refusal that arrived as somebody else's text is still a refusal, and the
// caller has to be able to tell it from a transport that failed: retrying a
// policy refusal changes nothing on any host.
func TestATrustRefusalSurvivesATransport(t *testing.T) {
	crossed := errors.New("it.cpak.Error.Failed: " + ErrTrustRefused.Error() + ": the administrator has not approved the origin")
	if !errors.Is(asRefusal(crossed), ErrTrustRefused) {
		t.Fatal("a trust refusal does not read back as itself after a transport")
	}
	if errors.Is(asRefusal(errors.New("the authority is not running")), ErrTrustRefused) {
		t.Fatal("an unrelated failure reads back as a trust refusal")
	}
}
