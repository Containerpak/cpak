/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	"net/url"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

const (
	// githubHost is the only host this version can turn a certificate into a
	// repository for. A certificate from anywhere else may be perfectly valid
	// and still speak for no cpak origin.
	githubHost = "github.com"

	// githubActionsIssuer is the OIDC issuer of GitHub Actions. Nothing else
	// issues a token that names a repository the way this design relies on.
	githubActionsIssuer = "https://token.actions.githubusercontent.com"
)

// Identity is who signed, taken from the certificate and never from the
// payload. A payload can say anything about itself; the certificate is what
// Fulcio issued against an OIDC token it checked, and it is the only part of a
// bundle that names a signer.
type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
	Repo    string `json:"repo"`
}

// MatchesOrigin reports whether this identity may speak for a package origin.
//
// The whole design rests on one string being two things at once: the origin
// cpak installs from, and the repository whose CI Fulcio wrote into the
// certificate. So the comparison is the whole of both, in the one shape an
// origin is allowed to have, and never a prefix, a suffix or a contains.
// github.com/acme/cpak is not github.com/acme/cpak-evil, not
// github.com/acme-inc/cpak, and not github.com/acme/cpak/anything.
//
// The issuer is part of the answer and not a detail. A repository name is only
// as good as the authority that put it in the certificate, and this version
// knows exactly one such authority.
func (i Identity) MatchesOrigin(origin string) bool {
	want, ok := canonicalOrigin(origin)
	if !ok {
		return false
	}
	if host, _, _ := strings.Cut(want, "/"); host != githubHost {
		return false
	}
	if i.Issuer != githubActionsIssuer {
		return false
	}
	repo, ok := canonicalOrigin(i.Repo)
	return ok && repo == want
}

// identityOf reads the signer out of a certificate that has already been
// verified. Nothing in here decides anything; it is a reading of fields the
// chain has just been proven over.
func identityOf(summary certificate.Summary) Identity {
	return Identity{
		Issuer:  summary.Extensions.Issuer,
		Subject: summary.SubjectAlternativeName,
		Repo:    repositoryOf(summary.Extensions.SourceRepositoryURI),
	}
}

// repositoryOf reads the repository out of the Fulcio source repository
// extension, which names what was built.
//
// It is deliberately not read out of the subject. On a reusable workflow the
// subject names the repository the workflow file lives in, which is a different
// repository from the one being released, and an origin check against it would
// let any repository that calls a shared workflow speak for the one that owns
// it.
func repositoryOf(sourceRepositoryURI string) string {
	if sourceRepositoryURI == "" {
		return ""
	}
	parsed, err := url.Parse(sourceRepositoryURI)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	repo, ok := canonicalOrigin(parsed.Host + "/" + path)
	if !ok {
		return ""
	}
	return repo
}
