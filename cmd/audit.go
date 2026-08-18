/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type AuditCmd struct {
	Repair           bool `cli:"repair" help:"Attempt to repair inconsistencies found in the store"`
	BackfillBindings bool `cli:"backfill-bindings" help:"Record a layer binding for every layer of an installed application that has none, trusting the store as it is now"`

	cli.Base
}

func (c *AuditCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak for audit: %w", err)
	}

	if c.BackfillBindings {
		if c.Repair {
			return fmt.Errorf("run --repair and --backfill-bindings one at a time")
		}
		return c.backfillBindings(&cp)
	}

	// The integrity section is printed whatever the store audit concluded. It
	// is the only place the records a launch is gated on are ever named, and a
	// store that just failed its audit is the store whose records matter most.
	auditErr := cp.Audit(c.Repair)
	reportErr := c.reportIntegrity(&cp)
	if auditErr != nil {
		return auditErr
	}
	return reportErr
}

// reportIntegrity says what the store records about itself, what it now holds,
// and what the anchor ledger claims about either. It changes nothing, with or
// without --repair: writing a record from the store as it stands is a separate
// act and it has to be asked for.
func (c *AuditCmd) reportIntegrity(cp *cpak.Cpak) error {
	report, err := cp.IntegrityReport()
	if err != nil {
		return fmt.Errorf("read what the store records about itself: %w", err)
	}
	// The ledger is the one part of this section the store does not own, and it
	// is read separately for that reason: a store that just failed its audit
	// has no say in what is recorded about it.
	anchors, err := cp.AnchorStates()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold: %w", err)
	}
	held := make(map[string]cpak.AnchorState, len(anchors))
	for _, state := range anchors {
		held[state.Origin] = state
	}
	// Who published an application is the one thing in this section that
	// neither the store nor this host can state: it is read out of the
	// signature the publisher attached, and proven again here rather than
	// believed from the record that holds it.
	signatures, err := cp.RecordedSignatures()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold about publishers: %w", err)
	}
	signed := make(map[string]cpak.RecordedSignature, len(signatures))
	for _, found := range signatures {
		signed[found.Origin] = found
	}
	c.Logger.Info("Integrity records")
	if len(report.Applications) == 0 {
		c.Logger.Info("  No application is installed.")
		return nil
	}
	for _, app := range report.Applications {
		c.reportApplication(app, held[app.Origin], signed[app.Origin])
	}
	c.summariseIntegrity(report, anchors, signatures)
	return nil
}

func (c *AuditCmd) reportApplication(app cpak.ApplicationIntegrity, anchor cpak.AnchorState, signed cpak.RecordedSignature) {
	name := app.Origin
	if app.Version != "" {
		name += " " + app.Version
	}
	if app.Unreadable != "" {
		c.Logger.Error("  %s: nothing could be read about it: %s", name, app.Unreadable)
		return
	}
	c.Logger.Info("  %s: %d of %d layers bound to a store state, %d of %d prepared checkouts described by the state they were made from",
		name, app.BoundLayers, app.Layers, app.DescribedCheckouts, app.PreparedCheckouts)
	c.reportEnrolment(anchor)
	c.reportSignature(signed)
	for _, disagreement := range app.Disagreements {
		c.Logger.Error("    the store contradicts itself: %s", disagreement)
	}
	for _, unmeasured := range app.Unmeasured {
		c.Logger.Warning("    nothing in the store speaks for it: %s", unmeasured)
	}
}

// reportEnrolment says what the anchor ledger holds for one application. It is
// the only line in this section that is not the store speaking about itself,
// which is exactly why it is here: every other count is a claim the account
// running the launch could have written.
func (c *AuditCmd) reportEnrolment(anchor cpak.AnchorState) {
	switch {
	case anchor.Unreadable != "":
		c.Logger.Error("    the integrity anchor could not be read: %s", anchor.Unreadable)
	case !anchor.Enrolled:
		c.Logger.Warning("    not enrolled: no integrity anchor says what a launch of it may be")
	case anchor.Underived != "":
		c.Logger.Error("    enrolled at generation %d, and nothing can be put next to that anchor: %s", anchor.Generation, anchor.Underived)
	case anchor.Recognised():
		c.Logger.Info("    enrolled at generation %d, and a launch derives the root the anchor holds", anchor.Generation)
	default:
		c.Logger.Error("    enrolled at generation %d, and a launch derives %s where the anchor holds %s", anchor.Generation, anchor.DerivedRoot, anchor.AnchorRoot)
	}
}

// reportSignature says who published one application, or that nobody said. It
// is printed only for an application the ledger answers for at all, because
// the line above has already said that nothing does.
func (c *AuditCmd) reportSignature(signed cpak.RecordedSignature) {
	switch {
	case !signed.Enrolled:
		return
	case signed.Verified:
		c.Logger.Info("    signed by %s at generation %d, and the signature verifies against the trust root cpak ships with", signed.Identity.Repo, signed.State.Generation)
	case signed.Unsigned():
		c.Logger.Warning("    unsigned: no publisher signature was recorded when it was enrolled")
	default:
		c.Logger.Error("    a publisher signature was recorded for it and no longer verifies: %v", signed.Reason)
	}
}

// summariseIntegrity closes the section. Every sentence here is written to be
// read by someone who would like to believe their store was checked, so none of
// them says it was.
func (c *AuditCmd) summariseIntegrity(report cpak.StoreIntegrity, anchors []cpak.AnchorState, signatures []cpak.RecordedSignature) {
	disagreements := report.Disagreements()
	unbound := report.UnboundLayers()
	undescribed := report.UndescribedCheckouts()
	unreadable := report.Unreadable()

	if disagreements > 0 {
		c.Logger.Error("The store contradicts itself about %s. Those applications do not start until it stops, and nothing in this command puts them back: install or update the application, which is the one act that ties a layer to the digest a registry named.", plural(disagreements, "layer"))
	}
	if unbound > 0 || undescribed > 0 {
		c.Logger.Warning("%s bound to no store state, %s described by none. Nothing in the store speaks for them, so a launch has nothing to compare and lets them through.", plural(unbound, "layer is"), plural(undescribed, "prepared checkout is"))
		c.Logger.Info("cpak audit --backfill-bindings binds each of those layers to the state the store holds for it at that moment. It checks nothing and it can find nothing: a layer somebody already altered is bound to the altered state, every launch afterwards derives from the altered state, and the alteration becomes the application. Binding a layer is also what gives its prepared checkout a shape to be described by, so the same sentence covers both counts.")
	}
	if unreadable > 0 {
		c.Logger.Warning("%s could not be read, so this pass says nothing at all about them.", plural(unreadable, "application"))
	}
	if disagreements == 0 && unbound == 0 && undescribed == 0 && unreadable == 0 {
		c.Logger.Success("Every layer is bound to a state the store holds, every prepared checkout has the shape that state describes, and nothing disagrees. That is the store agreeing with itself.")
	}
	c.summariseEnrolment(anchors)
	c.summariseSignatures(signatures)
	c.Logger.Info("What no line above says: that the store holds what the publisher shipped. Only the pull that downloaded a layer ties it to the digest a registry named. A binding written at any other moment took whatever the store already held, however it got there, and nothing you can read afterwards tells the two apart.")
	c.Logger.Info("An anchor adds one thing to that and no more: the application has not changed since it was enrolled. It is written from the installation exactly as it stood at that moment.")
	c.Logger.Info("A publisher signature adds a different thing: the manifest and the image that were installed came from the CI of the repository the package is published under, and were not altered on the way. It does not say the software is safe, and it does not survive a repository somebody else took over, because that repository is the identity being proven.")
	if c.Repair {
		c.Logger.Info("--repair writes none of these. A pull, an update and --backfill-bindings are the only things that do.")
	}
}

// summariseEnrolment closes the half of the section the store does not own. It
// names the enforcement level next to the counts, because unenrolled means
// nothing on its own: at off it costs the user nothing and at refuse it is the
// reason an application stopped starting.
func (c *AuditCmd) summariseEnrolment(anchors []cpak.AnchorState) {
	unenrolled, mismatched, unreadable := 0, 0, 0
	for _, anchor := range anchors {
		switch {
		case anchor.Unreadable != "":
			unreadable++
		case !anchor.Enrolled:
			unenrolled++
		case !anchor.Recognised():
			mismatched++
		}
	}
	level := systemauthority.Enforcement()
	c.Logger.Info("Verified launch enforcement is %s. cpak system enforcement says what each level does.", level)
	if mismatched > 0 {
		c.Logger.Error("%s enrolled and no longer the launch the anchor names. None of those starts at any enforcement level, and cpak system explain ORIGIN says which half moved.", plural(mismatched, "application is"))
	}
	if unenrolled > 0 {
		c.Logger.Warning("%s enrolled by nothing at all. %s", plural(unenrolled, "application is"), enrolmentConsequence(level))
		c.Logger.Info("Installing or updating an application enrols it, and records the installation exactly as it stands at that moment.")
	}
	if unreadable > 0 {
		c.Logger.Warning("The integrity anchor of %s could not be read, so this pass says nothing about what claims them.", plural(unreadable, "application"))
	}
}

// summariseSignatures closes the half nothing on this machine can state. It
// names the host policy next to the counts for the same reason the enforcement
// level is named: unsigned means nothing on its own, and at required it is the
// reason an installation was not enrolled.
func (c *AuditCmd) summariseSignatures(signatures []cpak.RecordedSignature) {
	signed, unsigned, failing := 0, 0, 0
	for _, found := range signatures {
		switch {
		case !found.Enrolled:
			continue
		case found.Verified:
			signed++
		case found.Unsigned():
			unsigned++
		default:
			failing++
		}
	}
	policy := systemauthority.Signatures()
	c.Logger.Info("Publisher signatures are %s on this host. cpak system signatures says what each policy does.", policy)
	if failing > 0 {
		c.Logger.Error("%s recorded with a publisher signature that no longer verifies. Either the trust root moved on under it or the record was changed, and until it is installed again nothing can say who published it.", plural(failing, "application is"))
	}
	if unsigned > 0 {
		c.Logger.Warning("%s enrolled with no publisher signature at all. %s", plural(unsigned, "application is"), signatureConsequence(policy))
	}
	if signed > 0 {
		c.Logger.Info("%s enrolled with a publisher signature that verifies. The identity above is the repository whose CI signed the state that was installed.", plural(signed, "application is"))
	}
}

// signatureConsequence turns the policy into what it costs, which is the only
// part of it a reader needs.
func signatureConsequence(policy systemauthority.SignaturePolicy) string {
	if policy == systemauthority.SignaturesRequired {
		return "At this policy an installation nobody signed is not enrolled at all, so those were enrolled before it was set."
	}
	return "At this policy they are enrolled and the record says they were unsigned. At required they would not be enrolled at all."
}

// enrolmentConsequence turns the level into the sentence that matters, which is
// what it costs the user rather than what it is called.
func enrolmentConsequence(level systemauthority.EnforcementLevel) string {
	switch level {
	case systemauthority.EnforcementRefuse:
		return "At this level an application nothing claims does not start."
	case systemauthority.EnforcementWarn:
		return "At this level it starts and every launch says so, and at refuse it would not start at all."
	}
	return "At this level it starts and nothing is said. At warn every launch would say so, and at refuse it would not start at all."
}

// plural counts a noun without the parenthesised s that makes a report read as
// though nobody wrote it. The plural is the singular with an s, and where a
// caller needs a verb it hands in the whole phrase.
func plural(count int, singular string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	if strings.HasSuffix(singular, " is") {
		return fmt.Sprintf("%d %ss are", count, strings.TrimSuffix(singular, " is"))
	}
	return fmt.Sprintf("%d %ss", count, singular)
}

// backfillBindings is asked for and never happens on its own, because it does
// not check anything: it writes down the layers as the store holds them at this
// moment. The warning is printed before the work, so that it is read by someone
// who can still stop.
func (c *AuditCmd) backfillBindings(cp *cpak.Cpak) error {
	c.Logger.Warning("This does not verify anything. It records every unbound layer as whatever the store holds right now, so a layer that was already modified is recorded as the layer. Install or update the application again if you need the registry to answer for its content.")

	report, err := cp.BackfillLayerBindings()
	if err != nil {
		return fmt.Errorf("backfill layer bindings: %w", err)
	}
	for _, layer := range report.Bound {
		c.Logger.Info("Recorded %s as the state the store holds now.", layer)
	}
	for _, refusal := range report.Refused {
		c.Logger.Error("Still unbound: %s: %s", refusal.Layer, refusal.Reason)
	}
	c.Logger.Info("Recorded %s, %d were already bound, %d are still unbound.", plural(len(report.Bound), "layer"), len(report.Unchanged), len(report.Refused))
	c.Logger.Info("What those bindings now say: the store has not changed since this command ran. They say nothing about what the publisher shipped, and they could not have found a change made before this command ran. From here on a launch derives what each prepared checkout must hold from the state that was just written down, whatever that state contains.")
	if len(report.Refused) > 0 {
		return fmt.Errorf("%d layers could not be bound", len(report.Refused))
	}
	return nil
}
