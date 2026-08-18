/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trustpolicy

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	hostPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	segmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// canonicalOrigin folds an origin into the one shape a policy may name: a host,
// an owner and a repository, lowercase, and nothing else.
//
// It is the fold signature applies to a signed state, held here as well rather
// than shared, because a policy has to be able to decide with nothing but
// itself. The two are still compared against each other: an origin an
// administrator approved is put next to a repository a certificate named, so
// the day these two folds disagree is the day a policy approves one string and
// enforces another. They are changed together or not at all.
//
// It reports failure rather than repairing what it was given. An origin here is
// what cpak resolved and never what a user typed, and a policy that quietly
// repaired a value would be deciding about something its caller never handed
// it.
func canonicalOrigin(value string) (string, bool) {
	value = foldASCII(value)
	parts := strings.Split(value, "/")
	if len(parts) != 3 || !hostPattern.MatchString(parts[0]) {
		return "", false
	}
	if !segmentPattern.MatchString(parts[1]) || !segmentPattern.MatchString(parts[2]) {
		return "", false
	}
	return value, true
}

// foldASCII lowercases the ASCII letters and touches nothing else.
// strings.ToLower would fold characters such as the Kelvin sign onto a plain k,
// which is exactly the trick an allowlist has to survive: a rune that is not an
// ASCII letter must stay what it is and be refused by the patterns above, never
// quietly become one.
func foldASCII(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

// sameOrigin reports whether two values are the same origin, whole value and
// after the one fold above. Anything either side cannot read as an origin is
// the same as nothing at all, itself included, so a malformed entry in a policy
// matches no package and a malformed package matches no entry.
func sameOrigin(first, second string) bool {
	left, ok := canonicalOrigin(first)
	if !ok {
		return false
	}
	right, ok := canonicalOrigin(second)
	return ok && left == right
}

// validIssuer reports whether a value is shaped like an OIDC issuer, which is
// always an https URL. The check exists so that a typo is refused while an
// administrator is still looking at it, rather than becoming an entry that
// silently matches nothing and a control that silently approves nobody.
func validIssuer(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return parsed.RawQuery == "" && parsed.Fragment == ""
}
