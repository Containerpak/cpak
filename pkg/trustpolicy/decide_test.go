/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trustpolicy

import (
	"strings"
	"testing"
)

func TestApprovedOriginsAllowOnlyWhatIsOnTheList(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedOrigins: []string{testOrigin, otherOrigin}}
	for _, origin := range []string{testOrigin, otherOrigin} {
		if decision := policy.AllowsOrigin(origin); !decision.Allowed {
			t.Fatalf("%s is on the administrator's list and must be installable: %s", origin, decision.Reason)
		}
	}
	decision := policy.AllowsOrigin("github.com/attacker/evil")
	if decision.Allowed {
		t.Fatalf("an origin the administrator never approved must not be installable")
	}
	if !strings.Contains(decision.Reason, "github.com/attacker/evil") {
		t.Fatalf("the refusal must name the origin it is about, got %q", decision.Reason)
	}
}

// GitHub treats a repository path case insensitively, so an origin that differs
// only in ASCII case is the same origin. Nothing else folds.
func TestApprovedOriginsAreComparedWholeValue(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedOrigins: []string{testOrigin}}
	if decision := policy.AllowsOrigin("GitHub.com/Acme/CPak"); !decision.Allowed {
		t.Fatalf("an origin differing only in ascii case is the same origin: %s", decision.Reason)
	}
	refused := map[string]string{
		"a repository the approved one is a prefix of": "github.com/acme/cpak-evil",
		"a repository the approved one is a suffix of": "github.com/acme/not-cpak",
		"a repository that contains the approved one":  "github.com/acme/xcpakx",
		"an owner the approved one is a prefix of":     "github.com/acme-inc/cpak",
		"an owner the approved one is a suffix of":     "github.com/notacme/cpak",
		"the owner and repository swapped":             "github.com/cpak/acme",
		"another forge with the same path":             "gitlab.com/acme/cpak",
		"a host the approved one is a suffix of":       "evilgithub.com/acme/cpak",
		"a host the approved one is a prefix of":       "github.com.evil.net/acme/cpak",
		"a repository with a path glued on":            "github.com/acme/cpak/evil",
		"a repository with a trailing slash":           "github.com/acme/cpak/",
		"a repository with a leading slash":            "/github.com/acme/cpak",
		"a repository with a space in it":              "github.com/acme/cp ak",
		// U+212A, the Kelvin sign, lowercases to a plain k under Unicode
		// folding. Two origins that are different byte strings must never fold
		// onto one.
		"a repository spelled with a homoglyph": "github.com/acme/cpa\u212a",
	}
	for name, origin := range refused {
		if decision := policy.AllowsOrigin(origin); decision.Allowed {
			t.Fatalf("%s was installed as though it were %s", name, testOrigin)
		}
	}
}

// The list is origins and not patterns on purpose, and an allowlist that
// accepts a prefix is where this kind of control usually breaks.
func TestApprovedOriginsAreNotPatterns(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedOrigins: []string{"github.com/acme/cpak"}}
	for _, origin := range []string{"github.com/acme/anything", "github.com/acme/cpak-extra"} {
		if decision := policy.AllowsOrigin(origin); decision.Allowed {
			t.Fatalf("%s was allowed by an entry that names one repository", origin)
		}
	}
}

func TestAValueThatIsNotAnOriginIsRefusedByEveryDecision(t *testing.T) {
	policy := managedPolicy()
	unreadable := []string{
		"",
		"acme/cpak",
		"github.com/acme",
		"github.com/acme/cpak/extra",
		"github.com//cpak",
		"github/acme/cpak",
		"https://github.com/acme/cpak",
		"github.com/acme/cpak/",
	}
	for _, origin := range unreadable {
		if decision := policy.AllowsOrigin(origin); decision.Allowed {
			t.Fatalf("%q is not an origin and must not be installable", origin)
		}
		if decision := policy.AllowsPublisher(testIssuer, testOrigin, origin); decision.Allowed {
			t.Fatalf("%q is not an origin, so nobody can be approved to publish it", origin)
		}
		if decision := policy.IsRevoked(origin, 1); decision.Allowed {
			t.Fatalf("%q is not an origin, so it cannot be known to be unrevoked", origin)
		}
	}
}

func TestAnApprovedSignerIsMatchedWholeValue(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: testIssuer, Repo: testOrigin}}}
	if decision := policy.AllowsPublisher(testIssuer, "GitHub.com/ACME/CPak", testOrigin); !decision.Allowed {
		t.Fatalf("a repository differing only in ascii case is the same repository: %s", decision.Reason)
	}
	refused := map[string][2]string{
		"another repository":                           {testIssuer, "github.com/attacker/evil"},
		"a repository the approved one is a prefix of": {testIssuer, "github.com/acme/cpak-evil"},
		"an owner the approved one is a prefix of":     {testIssuer, "github.com/acme-inc/cpak"},
		"a repository spelled with a homoglyph":        {testIssuer, "github.com/acme/cpa\u212a"},
		"another issuer entirely":                      {"https://accounts.google.com", testOrigin},
		"an issuer the approved one is a prefix of":    {"https://token.actions.githubusercontent.co", testOrigin},
		"a self hosted issuer that looks like it":      {"https://token.actions.githubusercontent.example.com", testOrigin},
		"an identity from no issuer at all":            {"", testOrigin},
		"an identity that named no repository":         {testIssuer, ""},
		"nobody at all":                                {"", ""},
	}
	for name, identity := range refused {
		if decision := policy.AllowsPublisher(identity[0], identity[1], testOrigin); decision.Allowed {
			t.Fatalf("%s was accepted as an approved publisher of %s", name, testOrigin)
		}
	}
}

// A signer list that is present and matches nothing refuses. It never falls
// through to allow, which is the failure that would make the whole list
// decorative.
func TestAPresentSignerListNeverFallsThroughToAllow(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: testIssuer, Repo: "github.com/acme/other"}}}
	if policy.RequirePublisher {
		t.Fatalf("the point of this case is that the list alone refuses, with no requirement set")
	}
	if decision := policy.AllowsPublisher(testIssuer, testOrigin, testOrigin); decision.Allowed {
		t.Fatalf("an identity no entry names must be refused even when no flag requires a publisher")
	}
}

// An organisation that releases everything from one workflow repository is why
// the signer repository is named separately from the origin.
func TestASignerNamedByRepositoryMayPublishAnotherOrigin(t *testing.T) {
	policy := Policy{
		ABI:              ABIVersion,
		RequirePublisher: true,
		ApprovedSigners:  []Signer{{Issuer: testIssuer, Repo: "github.com/acme/signing"}},
	}
	if decision := policy.AllowsPublisher(testIssuer, "github.com/acme/signing", testOrigin); !decision.Allowed {
		t.Fatalf("a signer the administrator named may publish an origin of its own: %s", decision.Reason)
	}
	if decision := policy.AllowsPublisher(testIssuer, testOrigin, testOrigin); decision.Allowed {
		t.Fatalf("an identity the administrator did not name must be refused even when it is the origin itself")
	}
}

// An entry that names no repository is "any repository", and any repository
// must not mean any repository may publish somebody else's origin.
func TestASignerWithNoRepositoryMayOnlySignItsOwnOrigin(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: testIssuer}}}
	if decision := policy.AllowsPublisher(testIssuer, testOrigin, testOrigin); !decision.Allowed {
		t.Fatalf("a publisher signing its own origin is what every honest release is: %s", decision.Reason)
	}
	decision := policy.AllowsPublisher(testIssuer, "github.com/attacker/evil", testOrigin)
	if decision.Allowed {
		t.Fatalf("an entry that names no repository let one repository publish another's origin")
	}
	if !strings.Contains(decision.Reason, testOrigin) {
		t.Fatalf("the refusal must name the origin the administrator has to act on, got %q", decision.Reason)
	}
	if decision := policy.AllowsPublisher(testIssuer, "", testOrigin); decision.Allowed {
		t.Fatalf("a certificate that named no repository cannot be the repository an origin is published from")
	}
}

func TestApprovalSignersDecideWhoMayCounterSign(t *testing.T) {
	policy := Policy{
		ABI:             ABIVersion,
		RequireApproval: true,
		ApprovalSigners: []Signer{{Issuer: testIssuer, Repo: "github.com/acme/approvals"}},
	}
	if decision := policy.AllowsApproval(testIssuer, "github.com/acme/approvals"); !decision.Allowed {
		t.Fatalf("the identity the administrator named must be allowed to counter-sign: %s", decision.Reason)
	}
	refused := map[string][2]string{
		"the publisher of the package itself":          {testIssuer, testOrigin},
		"a lookalike of the approval repository":       {testIssuer, "github.com/acme/approvals-evil"},
		"the approval repository under another issuer": {"https://accounts.google.com", "github.com/acme/approvals"},
		"nobody at all": {"", ""},
	}
	for name, identity := range refused {
		if decision := policy.AllowsApproval(identity[0], identity[1]); decision.Allowed {
			t.Fatalf("%s was allowed to counter-sign for this organisation", name)
		}
	}
}

// A host that names no approval signer expects no counter-signature, which is
// every host that has not asked for one.
func TestNoApprovalSignerMeansNoCounterSignatureIsExpected(t *testing.T) {
	policy := Policy{ABI: ABIVersion, ApprovedOrigins: []string{testOrigin}}
	if decision := policy.AllowsApproval(testIssuer, "github.com/acme/approvals"); !decision.Allowed {
		t.Fatalf("an organisation that named no approval signer must not refuse one: %s", decision.Reason)
	}
}

func TestRevocationBeatsApproval(t *testing.T) {
	policy := managedPolicy()
	policy.Revoked = []Revocation{{Origin: testOrigin, Reason: "the release runner was compromised"}}
	decision := policy.AllowsOrigin(testOrigin)
	if decision.Allowed {
		t.Fatalf("an origin the administrator approved and then revoked must not be installable")
	}
	if !strings.Contains(decision.Reason, "the release runner was compromised") {
		t.Fatalf("the refusal must carry the administrator's own words, got %q", decision.Reason)
	}
	for _, generation := range []uint64{0, 1, 4096} {
		if decision := policy.IsRevoked(testOrigin, generation); decision.Allowed {
			t.Fatalf("a revocation with no generation must withdraw generation %d as well", generation)
		}
	}
}

func TestARevocationOfOneGenerationLeavesTheOthers(t *testing.T) {
	policy := Policy{ABI: ABIVersion, Revoked: []Revocation{{Origin: testOrigin, Generation: 7}}}
	if decision := policy.IsRevoked(testOrigin, 7); decision.Allowed {
		t.Fatalf("the generation the administrator revoked must be withdrawn")
	}
	if decision := policy.IsRevoked(testOrigin, 8); !decision.Allowed {
		t.Fatalf("a generation nobody revoked must still stand: %s", decision.Reason)
	}
	if decision := policy.IsRevoked(otherOrigin, 7); !decision.Allowed {
		t.Fatalf("a revocation names one origin and must not reach another: %s", decision.Reason)
	}
	// Only a revocation of the whole origin can answer for an installation that
	// does not say which generation it is.
	if decision := policy.IsRevoked(testOrigin, 0); !decision.Allowed {
		t.Fatalf("a generation the administrator did not name must not withdraw an unknown one: %s", decision.Reason)
	}
	if decision := policy.AllowsOrigin(testOrigin); !decision.Allowed {
		t.Fatalf("revoking one generation must not stop the origin being installed at all: %s", decision.Reason)
	}
}

// The name says what is being looked up and not what true means. A caller that
// reads Allowed as "it is revoked" would run exactly the software that was
// withdrawn, so the polarity is nailed down here.
func TestIsRevokedReportsAllowedWhenTrustStillStands(t *testing.T) {
	policy := Policy{ABI: ABIVersion, Revoked: []Revocation{{Origin: otherOrigin}}}
	standing := policy.IsRevoked(testOrigin, 3)
	if !standing.Allowed {
		t.Fatalf("trust that was never withdrawn must report Allowed true: %s", standing.Reason)
	}
	withdrawn := policy.IsRevoked(otherOrigin, 3)
	if withdrawn.Allowed {
		t.Fatalf("trust that was withdrawn must report Allowed false")
	}
}

func TestARevocationIsMatchedWholeValue(t *testing.T) {
	policy := Policy{ABI: ABIVersion, Revoked: []Revocation{{Origin: testOrigin}}}
	if decision := policy.IsRevoked("GitHub.com/Acme/CPak", 1); decision.Allowed {
		t.Fatalf("an origin differing only in ascii case is the origin that was revoked")
	}
	for _, origin := range []string{"github.com/acme/cpak-evil", "github.com/acme-inc/cpak", "gitlab.com/acme/cpak"} {
		if decision := policy.IsRevoked(origin, 1); !decision.Allowed {
			t.Fatalf("%s is not the revoked origin and must not be withdrawn with it: %s", origin, decision.Reason)
		}
	}
}

// A malformed entry cannot be repaired into one that matches something else. It
// matches nothing, and the rest of the policy still decides.
func TestAMalformedEntryMatchesNothing(t *testing.T) {
	policy := Policy{
		ABI:             ABIVersion,
		ApprovedOrigins: []string{"not an origin", testOrigin},
		ApprovedSigners: []Signer{{Issuer: testIssuer, Repo: "not a repository"}},
		Revoked:         []Revocation{{Origin: "not an origin"}},
	}
	if decision := policy.AllowsOrigin(testOrigin); !decision.Allowed {
		t.Fatalf("a malformed entry must not stop a sound one deciding: %s", decision.Reason)
	}
	if decision := policy.AllowsPublisher(testIssuer, testOrigin, testOrigin); decision.Allowed {
		t.Fatalf("a signer entry that names no readable repository must approve nobody")
	}
	if decision := policy.IsRevoked(testOrigin, 1); !decision.Allowed {
		t.Fatalf("a revocation of nothing must withdraw nothing: %s", decision.Reason)
	}
}
