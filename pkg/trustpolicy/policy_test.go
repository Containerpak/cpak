/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trustpolicy

import (
	"errors"
	"strings"
	"testing"
)

const (
	testIssuer  = "https://token.actions.githubusercontent.com"
	testOrigin  = "github.com/acme/cpak"
	otherOrigin = "github.com/other/tool"
)

// managedPolicy is a host an organisation has actually decided about: one
// approved publisher, one approved origin, one identity that may counter-sign,
// and both requirements on.
func managedPolicy() Policy {
	return Policy{
		ABI:              ABIVersion,
		RequirePublisher: true,
		RequireApproval:  true,
		ApprovedSigners:  []Signer{{Issuer: testIssuer, Repo: testOrigin}},
		ApprovedOrigins:  []string{testOrigin},
		ApprovalSigners:  []Signer{{Issuer: testIssuer, Repo: "github.com/acme/approvals"}},
	}
}

// The invariant nothing in this package may break. Every installation that
// exists has no policy, so the absence of one has to behave exactly as cpak
// behaved before any of this was written.
func TestAnUnmanagedHostIsRefusedNothing(t *testing.T) {
	var policy Policy
	if !policy.Empty() {
		t.Fatalf("a policy nobody filled in must report itself empty")
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("the absence of a policy must be valid, got %v", err)
	}
	for _, origin := range []string{testOrigin, otherOrigin, "gitlab.com/anybody/anything"} {
		if decision := policy.AllowsOrigin(origin); !decision.Allowed {
			t.Fatalf("an unmanaged host must install %s: %s", origin, decision.Reason)
		}
	}
	unmanaged := map[string]Decision{
		"a publisher nobody approved":        policy.AllowsPublisher(testIssuer, "github.com/attacker/evil", testOrigin),
		"a package nobody signed":            policy.AllowsPublisher("", "", testOrigin),
		"an approval nobody gave":            policy.AllowsApproval("", ""),
		"a known generation":                 policy.IsRevoked(testOrigin, 7),
		"an installation with no generation": policy.IsRevoked(testOrigin, 0),
	}
	for name, decision := range unmanaged {
		if !decision.Allowed {
			t.Fatalf("an unmanaged host must not refuse %s: %s", name, decision.Reason)
		}
	}
}

// A policy file that exists and decides nothing is the same host as one that
// has no policy file at all.
func TestAPolicyThatDecidesNothingIsEmpty(t *testing.T) {
	policy := Policy{ABI: ABIVersion}
	if !policy.Empty() {
		t.Fatalf("naming an abi is not deciding anything, so the policy is still empty")
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("a policy that decides nothing is valid, got %v", err)
	}
	if decision := policy.AllowsOrigin("gitlab.com/anybody/anything"); !decision.Allowed {
		t.Fatalf("a policy that decides nothing must restrict nothing: %s", decision.Reason)
	}
}

func TestEveryDecisionOnAManagedHostCarriesAReason(t *testing.T) {
	policy := managedPolicy()
	policy.Revoked = []Revocation{{Origin: otherOrigin, Generation: 3}}
	decisions := []Decision{
		policy.AllowsOrigin(testOrigin),
		policy.AllowsOrigin(otherOrigin),
		policy.AllowsOrigin("not an origin"),
		policy.AllowsPublisher(testIssuer, testOrigin, testOrigin),
		policy.AllowsPublisher(testIssuer, "github.com/attacker/evil", testOrigin),
		policy.AllowsPublisher("", "", testOrigin),
		policy.AllowsApproval(testIssuer, "github.com/acme/approvals"),
		policy.AllowsApproval(testIssuer, "github.com/attacker/evil"),
		policy.IsRevoked(testOrigin, 1),
		policy.IsRevoked(otherOrigin, 3),
	}
	for index, decision := range decisions {
		if strings.TrimSpace(decision.Reason) == "" {
			t.Fatalf("decision %d carries no reason, and a reason is what an administrator reads", index)
		}
	}
}

func TestValidateAcceptsAPolicyAnAdministratorWouldWrite(t *testing.T) {
	policy := managedPolicy()
	policy.ApprovedOrigins = append(policy.ApprovedOrigins, otherOrigin)
	policy.Revoked = []Revocation{
		{Origin: testOrigin, Generation: 4, Reason: "the release was built from a compromised runner"},
		{Origin: "github.com/acme/retired"},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("a policy an administrator would write must be accepted, got %v", err)
	}
	if policy.Empty() {
		t.Fatalf("a policy that approves and revokes has decided something")
	}
}

func TestValidateRefusesAPolicyThatCannotMeanAnything(t *testing.T) {
	refused := map[string]Policy{
		"an abi this build does not know":                  {ABI: ABIVersion + 1, ApprovedOrigins: []string{testOrigin}},
		"a policy that decides without naming an abi":      {ApprovedOrigins: []string{testOrigin}},
		"a negative abi":                                   {ABI: -1, ApprovedOrigins: []string{testOrigin}},
		"a publisher requirement with nobody approved":     {ABI: ABIVersion, RequirePublisher: true},
		"an approval requirement with nobody named":        {ABI: ABIVersion, RequireApproval: true},
		"an approved signer that names nobody":             {ABI: ABIVersion, ApprovedSigners: []Signer{{}}},
		"an approval signer that names nobody":             {ABI: ABIVersion, ApprovalSigners: []Signer{{}}},
		"an approval signer that names no repository":      {ABI: ABIVersion, ApprovalSigners: []Signer{{Issuer: testIssuer}}},
		"an issuer that is not a url":                      {ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: "token.actions.githubusercontent.com"}}},
		"an issuer over plain http":                        {ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: "http://token.actions.githubusercontent.com"}}},
		"an issuer with a password in it":                  {ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: "https://user:pass@token.actions.githubusercontent.com"}}},
		"an issuer with trailing whitespace":               {ABI: ABIVersion, ApprovedSigners: []Signer{{Issuer: testIssuer + " "}}},
		"a signer repository that is only an owner":        {ABI: ABIVersion, ApprovedSigners: []Signer{{Repo: "github.com/acme"}}},
		"a signer repository written as a url":             {ABI: ABIVersion, ApprovedSigners: []Signer{{Repo: "https://github.com/acme/cpak"}}},
		"an approved origin that is not an origin":         {ABI: ABIVersion, ApprovedOrigins: []string{"https://github.com/acme/cpak"}},
		"an approved origin with a path glued on":          {ABI: ABIVersion, ApprovedOrigins: []string{"github.com/acme/cpak/extra"}},
		"an approved origin not written as it is compared": {ABI: ABIVersion, ApprovedOrigins: []string{"GitHub.com/Acme/CPak"}},
		"an approved origin spelled with a homoglyph":      {ABI: ABIVersion, ApprovedOrigins: []string{"github.com/acme/cpa\u212a"}},
		"a revocation of nothing":                          {ABI: ABIVersion, Revoked: []Revocation{{}}},
		"a revocation reason with a newline in it":         {ABI: ABIVersion, Revoked: []Revocation{{Origin: testOrigin, Reason: "taken\nover"}}},
		"a revocation reason nobody could read":            {ABI: ABIVersion, Revoked: []Revocation{{Origin: testOrigin, Reason: strings.Repeat("a", reasonLimit+1)}}},
	}
	for name, policy := range refused {
		err := policy.Validate()
		if err == nil {
			t.Fatalf("%s must be refused while an administrator is still looking at it", name)
		}
		if !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("%s must be reported as an invalid policy, got %v", name, err)
		}
	}
}

// A policy written for a later cpak parses into fields this build cannot see.
// Reading it as an absent policy is how the whole control would switch itself
// off on the machines that upgrade last.
func TestAPolicyFromALaterCpakRefusesInsteadOfAllowing(t *testing.T) {
	policy := Policy{ABI: ABIVersion + 1}
	if err := policy.Validate(); err == nil {
		t.Fatalf("a policy this build cannot read must be reported to the administrator")
	}
	decisions := map[string]Decision{
		"an origin":    policy.AllowsOrigin(testOrigin),
		"a publisher":  policy.AllowsPublisher(testIssuer, testOrigin, testOrigin),
		"an approval":  policy.AllowsApproval(testIssuer, "github.com/acme/approvals"),
		"a revocation": policy.IsRevoked(testOrigin, 1),
	}
	for name, decision := range decisions {
		if decision.Allowed {
			t.Fatalf("%s was decided under an abi this build cannot read: %s", name, decision.Reason)
		}
		if !strings.Contains(decision.Reason, "abi") {
			t.Fatalf("%s must say the abi is why, got %q", name, decision.Reason)
		}
	}
}

// The require flags are refused by Validate without a list, so the only way to
// reach a decision holding one is a loader that skipped Validate. It must not
// be the way the requirement is escaped.
func TestARequirementWithNobodyApprovedRefusesAtTheDecisionToo(t *testing.T) {
	publisher := Policy{ABI: ABIVersion, RequirePublisher: true}
	if decision := publisher.AllowsPublisher(testIssuer, testOrigin, testOrigin); decision.Allowed {
		t.Fatalf("a host that requires an approved publisher and approves none must refuse: %s", decision.Reason)
	}
	approval := Policy{ABI: ABIVersion, RequireApproval: true}
	if decision := approval.AllowsApproval(testIssuer, "github.com/acme/approvals"); decision.Allowed {
		t.Fatalf("a host that requires an approval and names nobody must refuse: %s", decision.Reason)
	}
}

func TestValidIssuerAcceptsTheIssuersAnOrganisationUses(t *testing.T) {
	for _, issuer := range []string{
		testIssuer,
		"https://accounts.google.com",
		"https://gitlab.com",
		"https://login.microsoftonline.com/00000000-0000-0000-0000-000000000000/v2.0",
	} {
		if !validIssuer(issuer) {
			t.Fatalf("%q is an issuer an organisation really uses and must be accepted", issuer)
		}
	}
	for _, issuer := range []string{"", " ", "https://", "not a url", "http://accounts.google.com", "https://accounts.google.com?a=b", "https://accounts.google.com#f"} {
		if validIssuer(issuer) {
			t.Fatalf("%q is not an oidc issuer and must be refused where it is written", issuer)
		}
	}
}
