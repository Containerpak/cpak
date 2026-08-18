/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trustpolicy

import "fmt"

// The four decisions below are the product. Each starts from the same two
// questions, compares whole values, and says why in a sentence somebody can act
// on, because these strings are what an administrator reads when an application
// will not start.
//
// They are four questions and not one on purpose. A caller asks the ones its
// moment can answer: an install knows an origin before it knows anything else,
// an enrolment holds a verified publisher identity, an approval is a second
// record that may or may not have arrived, and a launch knows the generation it
// is about to run. A single answer would force every caller to hold everything.

// AllowsOrigin reports whether software from an origin may be installed on this
// host at all. It is the gate that stops a managed machine being one command
// away from arbitrary software.
//
// The list is origins and never patterns. A revocation of every generation is
// answered here as well, because trust that was withdrawn must not stay
// reachable through a gate that reads only the approvals.
func (p Policy) AllowsOrigin(origin string) Decision {
	if decision, decided := p.settled(); decided {
		return decision
	}
	wanted, ok := canonicalOrigin(origin)
	if !ok {
		return refuse("%q is not a host/owner/repository origin, so nothing in this policy can name it", origin)
	}
	// Revocation beats approval. Only a revocation of the whole origin can be
	// answered here, because an origin is not a generation; IsRevoked answers
	// the rest.
	for _, revocation := range p.Revoked {
		if revocation.Generation == 0 && sameOrigin(revocation.Origin, wanted) {
			return refuse("the administrator revoked every generation of %s%s", wanted, because(revocation.Reason))
		}
	}
	if len(p.ApprovedOrigins) == 0 {
		return allow("the administrator did not restrict which origins may be installed")
	}
	for _, approved := range p.ApprovedOrigins {
		if sameOrigin(approved, wanted) {
			return allow("%s is one of the origins the administrator approved", wanted)
		}
	}
	return refuse("%s is not one of the %d origins the administrator approved", wanted, len(p.ApprovedOrigins))
}

// AllowsPublisher reports whether an identity may be the publisher of an
// origin. The identity is the one a verified certificate named and never
// anything a package said about itself.
//
// A signer entry that names a repository is the administrator naming exactly
// who may sign, which is how an organisation that releases everything from one
// workflow repository says so. An entry that names no repository is the
// administrator saying they do not mind which repository, and that must not be
// read as one repository being allowed to publish another's origin: for those
// entries the signing repository has to be the origin itself, which is what
// every honest publisher already is.
func (p Policy) AllowsPublisher(issuer, repo, origin string) Decision {
	if decision, decided := p.settled(); decided {
		return decision
	}
	wanted, ok := canonicalOrigin(origin)
	if !ok {
		return refuse("%q is not a host/owner/repository origin, so nothing in this policy can name it", origin)
	}
	// An identity with neither half is not an identity, and Signer treats an
	// empty field as "any", so this is the one case that has to be refused
	// before any entry is looked at.
	if issuer == "" && repo == "" {
		return refuse("nothing signed %s, so no approved publisher speaks for it", wanted)
	}
	if len(p.ApprovedSigners) == 0 {
		if p.RequirePublisher {
			return refuse("this host requires an approved publisher and the administrator approved none, so nobody can publish %s", wanted)
		}
		return allow("the administrator did not restrict who may publish")
	}
	openEntry := false
	for _, signer := range p.ApprovedSigners {
		if !signer.matches(issuer, repo) {
			continue
		}
		if signer.Repo != "" {
			return allow("%s is a publisher the administrator approved", identityLabel(issuer, repo))
		}
		if sameOrigin(repo, wanted) {
			return allow("%s is a publisher the administrator approved and is the repository %s is published from", identityLabel(issuer, repo), wanted)
		}
		openEntry = true
	}
	if openEntry {
		return refuse("the administrator approved publishers that sign their own origin, and %s is not the repository %s is published from", identityLabel(issuer, repo), wanted)
	}
	return refuse("%s is not one of the %d publishers the administrator approved", identityLabel(issuer, repo), len(p.ApprovedSigners))
}

// AllowsApproval reports whether an identity may counter-sign on behalf of this
// organisation. It is the part that stops a signature being an end in itself: a
// publisher signature is the publisher asserting themselves, while an approval
// is a second party stating that this exact state was examined here and
// accepted.
//
// It takes no origin because an approval identity has nothing to do with the
// package. It is the organisation, and the organisation is the same one
// whatever it is approving.
func (p Policy) AllowsApproval(issuer, repo string) Decision {
	if decision, decided := p.settled(); decided {
		return decision
	}
	if issuer == "" && repo == "" {
		return refuse("nothing counter-signed this state, so no approval identity speaks for it")
	}
	if len(p.ApprovalSigners) == 0 {
		if p.RequireApproval {
			return refuse("this host requires an approval and the administrator named nobody who may give one")
		}
		return allow("the administrator named no approval signer, so no counter-signature is expected")
	}
	for _, signer := range p.ApprovalSigners {
		if signer.matches(issuer, repo) {
			return allow("%s may counter-sign for this organisation", identityLabel(issuer, repo))
		}
	}
	return refuse("%s is not one of the %d identities the administrator allows to counter-sign", identityLabel(issuer, repo), len(p.ApprovalSigners))
}

// IsRevoked reports whether trust in a generation of an origin still stands.
//
// Read the answer the way every other answer here is read: Allowed is true when
// the launch may go ahead and false when the administrator has withdrawn it.
// The name says what is being looked up and not what true means, so a caller
// that refuses on Allowed being true has it exactly backwards.
//
// A generation of zero is an installation that does not say which generation it
// is, and only a revocation of the whole origin can answer for one. An
// administrator who has to stop something they cannot pin to a generation
// revokes the origin, which is the answer AllowsOrigin gives as well.
func (p Policy) IsRevoked(origin string, generation uint64) Decision {
	if decision, decided := p.settled(); decided {
		return decision
	}
	wanted, ok := canonicalOrigin(origin)
	if !ok {
		return refuse("%q is not a host/owner/repository origin, so nothing in this policy can name it", origin)
	}
	for _, revocation := range p.Revoked {
		if !sameOrigin(revocation.Origin, wanted) {
			continue
		}
		if revocation.Generation == 0 {
			return refuse("the administrator revoked every generation of %s%s", wanted, because(revocation.Reason))
		}
		if generation != 0 && revocation.Generation == generation {
			return refuse("the administrator revoked generation %d of %s%s", generation, wanted, because(revocation.Reason))
		}
	}
	if generation == 0 {
		return allow("the administrator did not revoke every generation of %s, and this installation does not say which generation it is", wanted)
	}
	return allow("the administrator did not revoke generation %d of %s", generation, wanted)
}

// settled answers the two questions every decision starts with: whether this
// build can read the policy at all, and whether there is anything in it to
// apply. It reports decided true when the answer does not depend on what was
// asked.
//
// An ABI this build does not know refuses and an absent one does not, because
// they are not the same situation. A policy that names no ABI was written where
// the field was not thought about and every field in it is one this build can
// read; a policy that names a later ABI has fields this build cannot see, so
// applying only the visible ones is how a control quietly turns itself off. It
// is the answer the enforcement level already gives next door: absent is
// permissive, present and unreadable is not.
func (p Policy) settled() (Decision, bool) {
	if p.ABI != 0 && p.ABI != ABIVersion {
		return refuse("this cpak enforces trust policy abi %d and this host holds abi %d, so none of it can be applied", ABIVersion, p.ABI), true
	}
	if p.Empty() {
		return allow("no administrator trust policy is in force on this host"), true
	}
	return Decision{}, false
}

// matches reports whether an identity is this signer. An empty field is the
// administrator saying they do not mind about that half; a field that is set is
// compared whole, with the same hostility to a lookalike an origin is compared
// with, never a prefix and never a contains.
//
// The issuer is compared byte for byte and never folded. A repository name is
// only as good as the authority that put it in the certificate, so an issuer
// that is nearly the right one is a different authority.
func (s Signer) matches(issuer, repo string) bool {
	if s.Issuer != "" && s.Issuer != issuer {
		return false
	}
	if s.Repo == "" {
		return true
	}
	return sameOrigin(s.Repo, repo)
}

// identityLabel names an identity the way an administrator would have to write
// it into the policy to approve it, which is the one thing they can do with the
// message.
func identityLabel(issuer, repo string) string {
	switch {
	case repo != "" && issuer != "":
		return fmt.Sprintf("%q issued by %q", repo, issuer)
	case repo != "":
		return fmt.Sprintf("%q", repo)
	default:
		return fmt.Sprintf("an identity issued by %q", issuer)
	}
}

// because carries the administrator's own words into the answer. It is usually
// the only part of a refusal that says what happened rather than what the rule
// is.
func because(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func allow(reason string, args ...any) Decision {
	return Decision{Allowed: true, Reason: fmt.Sprintf(reason, args...)}
}

func refuse(reason string, args ...any) Decision {
	return Decision{Reason: fmt.Sprintf(reason, args...)}
}
