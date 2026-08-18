/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

const testOrigin = "github.com/acme/cpak"

func githubIdentity(repo string) Identity {
	return Identity{
		Issuer:  githubActionsIssuer,
		Subject: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
		Repo:    repo,
	}
}

func TestMatchesOriginAcceptsTheRepositoryItself(t *testing.T) {
	if !githubIdentity(testOrigin).MatchesOrigin(testOrigin) {
		t.Fatalf("the repository in the certificate is the origin, so it must be allowed to speak for it")
	}
}

// GitHub treats a repository path case insensitively, so a certificate that
// carries the owner's preferred casing still names the same repository.
func TestMatchesOriginIgnoresCase(t *testing.T) {
	if !githubIdentity("github.com/ACME/CPak").MatchesOrigin(testOrigin) {
		t.Fatalf("a repository differing only in case is the same repository")
	}
	if !githubIdentity(testOrigin).MatchesOrigin("GitHub.com/Acme/CPAK") {
		t.Fatalf("an origin differing only in case is the same origin")
	}
}

func TestMatchesOriginRefusesEveryLookalike(t *testing.T) {
	refused := map[string]Identity{
		"a lookalike owner":                        githubIdentity("github.com/acrne/cpak"),
		"an owner with a doubled letter":           githubIdentity("github.com/accme/cpak"),
		"an owner the real one is a prefix of":     githubIdentity("github.com/acme-inc/cpak"),
		"an owner the real one is a suffix of":     githubIdentity("github.com/notacme/cpak"),
		"a repository the real one is a prefix of": githubIdentity("github.com/acme/cpak-evil"),
		"a repository the real one is a suffix of": githubIdentity("github.com/acme/not-cpak"),
		"a repository that contains the real one":  githubIdentity("github.com/acme/xcpakx"),
		"a repository with a path glued on":        githubIdentity("github.com/acme/cpak/evil"),
		"a repository with a trailing slash":       githubIdentity("github.com/acme/cpak/"),
		"a repository with a leading slash":        githubIdentity("/github.com/acme/cpak"),
		"the owner and repository swapped":         githubIdentity("github.com/cpak/acme"),
		"another forge with the same path":         githubIdentity("gitlab.com/acme/cpak"),
		"a host the real one is a suffix of":       githubIdentity("evilgithub.com/acme/cpak"),
		"a host the real one is a prefix of":       githubIdentity("github.com.evil.net/acme/cpak"),
		"a certificate that names no repository":   githubIdentity(""),
		"a repository with a space":                githubIdentity("github.com/acme/cp ak"),
		"an identity from another issuer": {
			Issuer:  "https://accounts.google.com",
			Subject: "someone@example.com",
			Repo:    testOrigin,
		},
		"an identity from a self hosted actions issuer": {
			Issuer:  "https://token.actions.githubusercontent.example.com",
			Subject: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
			Repo:    testOrigin,
		},
		"an identity from an issuer that is a prefix of the real one": {
			Issuer:  "https://token.actions.githubusercontent.co",
			Subject: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
			Repo:    testOrigin,
		},
		"an identity with no issuer at all": {
			Subject: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
			Repo:    testOrigin,
		},
		"the zero identity": {},
		// U+212A, the Kelvin sign, lowercases to a plain k under Unicode
		// folding. Two origins that are different byte strings must never
		// fold onto one.
		"a repository spelled with a homoglyph": githubIdentity("github.com/acme/cpa\u212a"),
	}
	for name, identity := range refused {
		if identity.MatchesOrigin(testOrigin) {
			t.Fatalf("%s was allowed to speak for %s", name, testOrigin)
		}
	}
}

// The subject is where a workflow path lives, and a workflow path can be made
// to contain any string at all. Only the repository decides.
func TestMatchesOriginIgnoresTheSubject(t *testing.T) {
	identity := Identity{
		Issuer:  githubActionsIssuer,
		Subject: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
		Repo:    "github.com/attacker/pretender",
	}
	if identity.MatchesOrigin(testOrigin) {
		t.Fatalf("a subject that names the victim repository must not let another repository sign for it")
	}
}

func TestMatchesOriginRefusesAnOriginItCannotRead(t *testing.T) {
	identity := githubIdentity(testOrigin)
	for _, origin := range []string{"", "acme/cpak", "github.com/acme", "github.com/acme/cpak/extra", "github.com//cpak", "github/acme/cpak"} {
		if identity.MatchesOrigin(origin) {
			t.Fatalf("an origin that is not a host, owner and repository was matched: %q", origin)
		}
	}
}

func TestIdentityIsReadFromTheCertificate(t *testing.T) {
	summary := certificate.Summary{
		SubjectAlternativeName: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
		Extensions: certificate.Extensions{
			Issuer:              githubActionsIssuer,
			SourceRepositoryURI: "https://github.com/Acme/CPak",
		},
	}

	identity := identityOf(summary)
	if identity.Issuer != githubActionsIssuer {
		t.Fatalf("the issuer must come from the certificate extension, got %q", identity.Issuer)
	}
	if identity.Subject != summary.SubjectAlternativeName {
		t.Fatalf("the subject must be the certificate subject alternative name, got %q", identity.Subject)
	}
	if identity.Repo != testOrigin {
		t.Fatalf("the repository must be read from the source repository extension and folded to an origin, got %q", identity.Repo)
	}
	if !identity.MatchesOrigin(testOrigin) {
		t.Fatalf("an identity read from a GitHub Actions certificate for %s must speak for it", testOrigin)
	}
}

// A certificate that carries no source repository extension names no
// repository. It must not fall back to the subject, and it must not match.
func TestIdentityWithoutTheSourceRepositoryExtensionSpeaksForNothing(t *testing.T) {
	summary := certificate.Summary{
		SubjectAlternativeName: "https://github.com/acme/cpak/.github/workflows/release.yml@refs/heads/main",
		Extensions:             certificate.Extensions{Issuer: githubActionsIssuer},
	}

	identity := identityOf(summary)
	if identity.Repo != "" {
		t.Fatalf("nothing in the certificate named a repository, so none may be reported: %q", identity.Repo)
	}
	if identity.MatchesOrigin(testOrigin) {
		t.Fatalf("a certificate that names no repository was allowed to speak for %s", testOrigin)
	}
}

func TestRepositoryIsReadOnlyFromAUsableURI(t *testing.T) {
	unusable := map[string]string{
		"an empty extension":         "",
		"a plain http uri":           "http://github.com/acme/cpak",
		"a uri with no scheme":       "github.com/acme/cpak",
		"a uri with no path":         "https://github.com",
		"a uri with only an owner":   "https://github.com/acme",
		"a uri with too many parts":  "https://github.com/acme/cpak/tree/main",
		"a uri with an empty owner":  "https://github.com//cpak",
		"a uri that is not a uri":    "https://%zz",
		"a uri with a userinfo host": "https://user@github.com/acme/cpak",
	}
	for name, value := range unusable {
		if repo := repositoryOf(value); repo != "" {
			t.Fatalf("%s must name no repository, got %q", name, repo)
		}
	}
	if repo := repositoryOf("https://github.com/acme/cpak.git"); repo != testOrigin {
		t.Fatalf("a source repository uri with a .git suffix names the same repository, got %q", repo)
	}
}
