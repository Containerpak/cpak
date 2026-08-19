/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/trustpolicy"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemCmd struct {
	Action string `arg:"action" help:"Action: setup, remove, status, enforcement, set-enforcement, signatures, set-signatures, trust, set-trust, ceiling, set-ceiling, explain, clear-removal, register-session or remove-session"`
	Target string `arg:"target" help:"Enforcement level for set-enforcement, signature policy for set-signatures, policy file for set-trust and set-ceiling, package origin for explain and clear-removal"`

	ID          string `cli:"id" help:"Session identifier for the session actions"`
	Origin      string `cli:"origin" help:"Package origin for the session actions"`
	Name        string `cli:"name" help:"Session name for register-session"`
	Description string `cli:"description" help:"Session description for register-session"`
	Kind        string `cli:"kind" help:"Session kind for register-session"`

	cli.Base
}

func (c *SystemCmd) Run() error {
	action := strings.ToLower(c.Action)
	switch action {
	case "status":
		if systemauthority.Installed() {
			c.Logger.Success("cpak system integration is installed")
			return nil
		}
		return fmt.Errorf("cpak system integration is not installed")
	case "setup", "remove":
		if os.Geteuid() != 0 {
			return runSystemSetup(action)
		}
		if action == "setup" {
			pending, err := systemauthority.Install()
			for _, note := range pending {
				c.Logger.Warning(note)
			}
			return err
		}
		if err := systemauthority.Uninstall(); err != nil {
			if errors.Is(err, systemauthority.ErrNotInstalled) {
				c.Logger.Info("cpak system integration is not installed, nothing to remove")
				return nil
			}
			return err
		}
		c.Logger.Success("cpak system integration removed")
		return nil
	case "trust":
		return c.reportTrust()
	case "set-trust":
		return c.setTrust()
	case "ceiling":
		return c.reportCeiling()
	case "set-ceiling":
		return c.setCeiling()
	case "enforcement":
		return c.reportEnforcement()
	case "set-enforcement":
		return c.setEnforcement()
	case "signatures":
		return c.reportSignaturePolicy()
	case "set-signatures":
		return c.setSignaturePolicy()
	case "explain":
		return c.explain()
	case "clear-removal":
		return c.clearRemoval()
	case "register-session", "remove-session":
		// These carry a step already validated against the store of the user
		// who asked for it, so they only ever run through the escalation.
		if os.Geteuid() != 0 {
			return fmt.Errorf("%s requires root", action)
		}
		if action == "register-session" {
			return systemauthority.Register(systemauthority.Session{
				ID:          c.ID,
				Origin:      c.Origin,
				Name:        c.Name,
				Description: c.Description,
				Kind:        c.Kind,
			})
		}
		registered, err := systemauthority.DefaultRegistry().Load(c.ID)
		if err != nil {
			return err
		}
		return systemauthority.Remove(registered.ID, registered.Origin)
	default:
		return fmt.Errorf("unsupported system action %q", c.Action)
	}
}

func runSystemSetup(action string) error {
	if err := runPrivileged("system", action); err != nil {
		return fmt.Errorf("system integration %s failed: %w", action, err)
	}
	return nil
}

// reportEnforcement says what this host does about an application no anchor
// answers for, and what the other two levels would do instead. The three
// sentences are printed every time because the level is the whole of the
// decision and nobody remembers which of the three they left it on.
func (c *SystemCmd) reportEnforcement() error {
	c.Logger.Info("Verified launch enforcement is %s.", systemauthority.Enforcement())
	c.Logger.Info("  off     an application no anchor answers for starts, and nothing is said")
	c.Logger.Info("  warn    it starts, and every launch says on the error stream what refuse would have refused")
	c.Logger.Info("  refuse  it does not start")
	c.Logger.Info("No level changes what happens to an application the store contradicts itself about, or to one that is not the launch its anchor names: those do not start at any level.")
	c.Logger.Info("cpak audit says which applications are enrolled, and cpak system explain ORIGIN says what the ledger holds for one of them.")
	c.Logger.Info("What gets an application enrolled in the first place is decided separately, by cpak system signatures.")
	return nil
}

// reportSignaturePolicy says what this host does about a package no publisher
// signed, and then how to see the whole of it work. The walkthrough is printed
// because the two settings only make sense together: this one decides what is
// enrolled, the enforcement level decides what an unenrolled application may
// still do, and neither sentence is much use without the other.
func (c *SystemCmd) reportSignaturePolicy() error {
	c.Logger.Info("Publisher signatures are %s.", systemauthority.Signatures())
	c.Logger.Info("  optional  an installation nobody signed is enrolled, and the record says it was unsigned")
	c.Logger.Info("  required  an installation nobody signed is not enrolled at all: the software stays installed and answers to nothing")
	c.Logger.Info("A signature says the manifest and the image that were installed came from the CI of the repository the package is published under, and were not altered on the way. It does not say the software is safe, and it does not survive a repository somebody else took over.")
	c.Logger.Info("This policy decides enrolment and never a launch. What happens to an application nothing has enrolled is the enforcement level, which cpak system enforcement sets.")
	c.Logger.Info("To follow it end to end:")
	c.Logger.Info("  1. publish a signed release. docs/publishing-signatures.md has the workflow, and it needs no key and no secret: the identity in the certificate is the repository itself.")
	c.Logger.Info("  2. cpak install ORIGIN, then cpak audit. The application reads as signed by the repository that published it, at the generation the publisher counted.")
	c.Logger.Info("  3. cpak system set-signatures required.")
	c.Logger.Info("  4. install a package nobody signed. It installs, it is not enrolled, and it says so as it happens; cpak audit says it again afterwards.")
	c.Logger.Info("  5. cpak system set-signatures optional puts the host back where it was.")
	return nil
}

// setSignaturePolicy changes what this host takes, for every account on it, so
// it is the owner of the machine's decision and it goes through the authority.
func (c *SystemCmd) setSignaturePolicy() error {
	policy, err := signaturePolicyValue(c.Target)
	if err != nil {
		return err
	}
	escalated, err := c.applySignaturePolicy(policy)
	if err != nil {
		return fmt.Errorf("set the signature policy: %w", err)
	}
	if !escalated {
		c.Logger.Success("Publisher signatures are now %s.", policy)
	}
	return c.reportSignatureConsequences(policy)
}

func (c *SystemCmd) applySignaturePolicy(policy systemauthority.SignaturePolicy) (bool, error) {
	err := systemauthority.SetSignaturePolicy(policy)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, systemauthority.ErrNoAuthority) || os.Geteuid() == 0 {
		return false, err
	}
	return true, runPrivileged("system", "set-signatures", string(policy))
}

// reportSignatureConsequences says what the policy just set costs the
// applications that are installed now. Nothing is unenrolled by setting it,
// which is exactly why it has to be said: the cost arrives later, at the next
// install or update of an application no signature stands for.
func (c *SystemCmd) reportSignatureConsequences(policy systemauthority.SignaturePolicy) error {
	// Root here is the escalated step, whose store is not the one the question
	// is about. The process that escalated reads the right one and prints this.
	if policy != systemauthority.SignaturesRequired || os.Geteuid() == 0 {
		return nil
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold about publishers: %w", err)
	}
	signatures, err := cp.RecordedSignatures()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold about publishers: %w", err)
	}
	unsigned := make([]string, 0, len(signatures))
	for _, found := range signatures {
		if found.Enrolled && !found.Verified {
			unsigned = append(unsigned, found.Origin)
		}
	}
	if len(unsigned) == 0 {
		return nil
	}
	c.Logger.Warning("No publisher signature stands for these, and they are enrolled: %s", strings.Join(unsigned, ", "))
	c.Logger.Info("They keep the anchors they have and they keep starting. What changes is the next time one of them actually changes: the installation that comes out of it is not enrolled, it no longer matches the anchor it left behind, and from that moment the enforcement level decides what happens to it.")
	return nil
}

func signaturePolicyValue(value string) (systemauthority.SignaturePolicy, error) {
	switch systemauthority.SignaturePolicy(strings.ToLower(strings.TrimSpace(value))) {
	case systemauthority.SignaturesOptional:
		return systemauthority.SignaturesOptional, nil
	case systemauthority.SignaturesRequired:
		return systemauthority.SignaturesRequired, nil
	}
	return "", fmt.Errorf("unsupported signature policy %q: use optional or required", value)
}

// setEnforcement changes the level for every account on the host, so it is the
// owner of the machine's decision and it goes through the authority. Whatever
// set it, what it costs is printed by this process: it is the one that can read
// the store of the user who asked.
func (c *SystemCmd) setEnforcement() error {
	level, err := enforcementLevel(c.Target)
	if err != nil {
		return err
	}
	escalated, err := c.applyEnforcement(level)
	if err != nil {
		return fmt.Errorf("set the enforcement level: %w", err)
	}
	if !escalated {
		c.Logger.Success("Verified launch enforcement is now %s.", level)
	}
	return c.reportEnforcementConsequences(level)
}

// applyEnforcement asks the authority and re-enters cpak as root only when no
// transport could reach one, which is the order every other privileged step
// here follows. It says which of the two happened, because the escalated cpak
// has already reported what it set.
func (c *SystemCmd) applyEnforcement(level systemauthority.EnforcementLevel) (bool, error) {
	err := systemauthority.SetEnforcement(level)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, systemauthority.ErrNoAuthority) || os.Geteuid() == 0 {
		return false, err
	}
	return true, runPrivileged("system", "set-enforcement", string(level))
}

// reportEnforcementConsequences says what the level just set does to the
// applications that are installed now, because refuse is the one setting on
// this host that can stop software the user was running a minute ago.
func (c *SystemCmd) reportEnforcementConsequences(level systemauthority.EnforcementLevel) error {
	// Root here is the escalated step, whose store is not the one the question
	// is about. The process that escalated reads the right one and prints this.
	if level == systemauthority.EnforcementOff || os.Geteuid() == 0 {
		return nil
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold: %w", err)
	}
	states, err := cp.AnchorStates()
	if err != nil {
		return fmt.Errorf("read what the integrity anchors hold: %w", err)
	}
	unclaimed := make([]string, 0, len(states))
	for _, state := range states {
		if !state.Enrolled {
			unclaimed = append(unclaimed, state.Origin)
		}
	}
	if len(unclaimed) == 0 {
		return nil
	}
	if level == systemauthority.EnforcementRefuse {
		c.Logger.Error("These applications are not enrolled and no longer start: %s", strings.Join(unclaimed, ", "))
	} else {
		c.Logger.Warning("These applications are not enrolled and would not start at refuse: %s", strings.Join(unclaimed, ", "))
	}
	c.Logger.Info("Installing or updating an application enrols it. cpak system explain ORIGIN says what the ledger holds for one of them.")
	return nil
}

func enforcementLevel(value string) (systemauthority.EnforcementLevel, error) {
	switch systemauthority.EnforcementLevel(strings.ToLower(strings.TrimSpace(value))) {
	case systemauthority.EnforcementOff:
		return systemauthority.EnforcementOff, nil
	case systemauthority.EnforcementWarn:
		return systemauthority.EnforcementWarn, nil
	case systemauthority.EnforcementRefuse:
		return systemauthority.EnforcementRefuse, nil
	}
	return "", fmt.Errorf("unsupported enforcement level %q: use off, warn or refuse", value)
}

// clearRemoval gives up what the removal of an application left in the ledger.
//
// It is the way out of the one refusal an ordinary user cannot answer. The
// ledger keeps the generation an application had reached and whether a
// publisher had ever answered for it, so that removing it and installing
// something older or unsigned in its place is not a way past either rule. That
// is right, and it also outlives what it was derived from: a publisher that
// stops signing, a key that rotated, a repository that changed hands. From then
// on every install of that origin is refused for a signature no installation of
// it can produce, and nothing an installer does answers that. This does, and it
// asks for an administrator password, because what it gives up is a protection
// every account on the machine was under.
//
// It takes the origin as it was printed in the refusal, and it does not require
// the application to be installed: the state this exists for is one where the
// installation was refused enrolment, and it is just as reachable with nothing
// on disk at all.
func (c *SystemCmd) clearRemoval() error {
	if err := refuseSudoedStore(); err != nil {
		return err
	}
	if c.Target == "" {
		return errors.New("name the application whose removal is to be cleared: cpak system clear-removal ORIGIN")
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	origin, err := cp.ResolveOrigin(c.Target)
	if err != nil {
		return err
	}
	uid := uint32(os.Getuid())
	buried, entombed, err := systemauthority.ForgottenAnchor(uid, origin)
	if err != nil {
		return fmt.Errorf("read what the removal of %s left behind: %w", origin, err)
	}
	if !entombed {
		c.Logger.Info("The removal of %s left nothing in the ledger: there is nothing to clear.", origin)
		c.Logger.Info("An installation refused for something other than a removal is explained by cpak system explain %s.", origin)
		return nil
	}
	// Said before anybody is asked to authenticate, because an administrator
	// typing a password is entitled to know what it buys, and this one buys a
	// protection being given up rather than a permission being granted.
	c.reportRemovalFloor(origin, buried)
	if err := systemauthority.ClearForgottenAnchor(uid, origin); err != nil {
		return fmt.Errorf("clear what the removal of %s left behind: %w", origin, err)
	}
	c.Logger.Success("The ledger holds nothing from the removal of %s any more. Install it again to enrol it.", origin)
	return nil
}

// reportRemovalFloor says what is about to be given up, in the terms the next
// install will meet it in.
func (c *SystemCmd) reportRemovalFloor(origin string, buried systemauthority.Tombstone) {
	c.Logger.Warning("Clearing this gives up what the ledger kept when %s was removed:", origin)
	c.Logger.Info("  generation %d, which every enrolment of it has to stay above", buried.Generation)
	if buried.Signed {
		c.Logger.Info("  a publisher signature at generation %d, which every enrolment of it has to carry and not go below", buried.SignatureGeneration)
	} else {
		c.Logger.Info("  no publisher signature: nothing had ever signed this origin when it was removed")
	}
	c.Logger.Info("Once it is cleared, installing %s again can enrol it at any generation, under any policy and with no publisher signature at all, and none of that will be put to anybody. What it cannot do is reach past this host's trust policy, its ceiling or its signature policy, which decide separately and are not touched by this.", origin)
	c.Logger.Info("An anchor recorded since the removal is left exactly as it is: this gives up what a removal kept, never what a launch is recognised by.")
}

// explain puts what the ledger holds for one application next to what a launch
// of it derives right now. It exists because a refusal names the verdict and
// not the difference, and a person who cannot see the difference can only guess
// at which of the two sides moved.
func (c *SystemCmd) explain() error {
	if err := refuseSudoedStore(); err != nil {
		return err
	}
	if c.Target == "" {
		return errors.New("name the installed application to explain: cpak system explain ORIGIN")
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	origin, err := resolveApplicationOrigin(cp, c.Target)
	if err != nil {
		return err
	}
	explanation, err := cp.ExplainLaunch(origin)
	if err != nil {
		return err
	}
	c.reportExplanation(explanation)
	return nil
}

func (c *SystemCmd) reportExplanation(explanation cpak.LaunchExplanation) {
	name := explanation.Origin
	if explanation.Version != "" {
		name += " " + explanation.Version
	}
	c.Logger.Info("%s", name)
	c.Logger.Info("  Enforcement: %s", explanation.Enforcement)
	c.reportLedgerSide(explanation)
	c.reportPublisher(explanation.Origin)
	c.Logger.Info("  What a launch derives from the store now")
	c.reportRoots(explanation.Identity.LaunchRoot, explanation.Identity.PackageRoot, explanation.Identity.PolicyRoot)
	c.Logger.Info("  Verdict: %s", explanation.Identity.Verdict)
	for _, disagreement := range explanation.Identity.Disagreements {
		c.Logger.Error("    the store contradicts itself: %s", disagreement)
	}
	for _, unmeasured := range explanation.Identity.Unmeasured {
		c.Logger.Warning("    nothing in the store speaks for it: %s", unmeasured)
	}
	if explanation.Refusal != nil {
		c.Logger.Error("  This launch does not start: %v", explanation.Refusal)
	} else {
		c.Logger.Success("  This launch starts.")
	}
	c.Logger.Info("An anchor records the installation as it stood when it was written: it says the application has not changed since, and it does not say the store holds what the publisher shipped.")
	c.Logger.Info("A publisher signature is the other half and a different claim: the manifest and the image that were installed came from the CI of the repository the package is published under. An application enrolled without one was taken on trust at the moment it was installed.")
}

// reportPublisher says who signed the installation the ledger holds, proven
// again here and not read off the record.
func (c *SystemCmd) reportPublisher(origin string) {
	cp, err := cpak.NewCpak()
	if err != nil {
		c.Logger.Error("  Who published it could not be read: %v", err)
		return
	}
	found := cp.RecordedSignatureOf(origin)
	switch {
	case !found.Enrolled:
		return
	case found.Verified:
		c.Logger.Info("  Signed by %s, at the publisher generation %d", found.Identity.Repo, found.State.Generation)
	case found.Unsigned():
		c.Logger.Warning("  Unsigned: no publisher signature was recorded when it was enrolled")
	default:
		c.Logger.Error("  A publisher signature was recorded for it and no longer verifies: %v", found.Reason)
	}
}

func (c *SystemCmd) reportLedgerSide(explanation cpak.LaunchExplanation) {
	if explanation.AnchorReason != nil {
		c.Logger.Error("  The ledger would not answer: %v", explanation.AnchorReason)
		return
	}
	if !explanation.Enrolled {
		c.Logger.Warning("  The ledger holds nothing: no anchor claims what a launch of this application may be.")
		return
	}
	c.Logger.Info("  What the ledger holds, at generation %d", explanation.Anchor.Generation)
	c.reportRoots(explanation.Anchor.LaunchRoot, explanation.Anchor.PackageRoot, explanation.Anchor.PolicyRoot)
}

// reportRoots prints the two halves under the root they make, because the pair
// is what says which side of an application moved: the same policy root under a
// different package root is an installation that changed, and the reverse is a
// permission its owner changed.
func (c *SystemCmd) reportRoots(launchRoot, packageRoot, policyRoot string) {
	c.Logger.Info("    launch root  %s", orNothing(launchRoot))
	c.Logger.Info("    package root %s", orNothing(packageRoot))
	c.Logger.Info("    policy root  %s", orNothing(policyRoot))
}

// orNothing says a value is absent in words. An empty column reads as a value
// nobody looked at, and every empty root here is a root that could not be
// derived at all.
func orNothing(value string) string {
	if value == "" {
		return "could not be derived"
	}
	return value
}

// describeCeiling lists the permissions the ceiling actually names, and only
// those. Printing the whole policy would show a value for every field and read
// as a decision about all of them, when the file decided a handful.
func describeCeiling(ceiling systemauthority.Ceiling) []string {
	policy := reflect.ValueOf(ceiling.Policy)
	fields := policy.Type()
	lines := []string{}
	for index := 0; index < fields.NumField(); index++ {
		key := strings.Split(fields.Field(index).Tag.Get("json"), ",")[0]
		if key == "" || !ceiling.Named[key] {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: %v", key, policy.Field(index).Interface()))
	}
	sort.Strings(lines)
	return lines
}

// reportCeiling says what this host allows at most. It is not privileged: an
// application that will not do what its manifest promises is something the
// person running it has to be able to explain without asking anyone.
func (c *SystemCmd) reportCeiling() error {
	ceiling, err := systemauthority.DefaultCeilingStore().Load()
	if err != nil {
		return err
	}
	if !ceiling.Present {
		c.Logger.Info("This host sets no ceiling: an application is allowed what its manifest asks and its owner permits.")
		c.Logger.Info("cpak system set-ceiling FILE reads a policy from a cpak.json override file and makes it the maximum.")
		return nil
	}
	if len(ceiling.Named) == 0 {
		c.Logger.Info("This host sets a ceiling that names no permission, so it holds nothing back.")
		c.Logger.Info("cpak system set-ceiling none removes it.")
		return nil
	}
	c.Logger.Info("This host holds an application to the following, and to nothing else:")
	for _, line := range describeCeiling(ceiling) {
		c.Logger.Info(line)
	}
	c.Logger.Info("Whatever a manifest asks and whatever an owner permits is held to this, whoever published the application and whether or not it is signed.")
	c.Logger.Info("Every permission not listed is left to the manifest and the owner.")
	c.Logger.Info("cpak system set-ceiling none removes it.")
	return nil
}

// setCeiling takes a file rather than a flag per permission, because a ceiling
// is one decision about a whole policy and setting it a field at a time would
// leave the host in states nobody chose.
func (c *SystemCmd) setCeiling() error {
	if err := refuseSudoedStore(); err != nil {
		return err
	}
	if c.Target == "" {
		return fmt.Errorf("name the file the ceiling is read from, or none to remove it")
	}
	// The file is read and understood before anyone is asked to authenticate,
	// so a mistyped path costs a message and not an administrator password.
	//
	// The bytes travel rather than a decoded policy: which permissions the file
	// leaves out is what decides how far the ceiling reaches, and a struct
	// cannot carry that.
	var policy []byte
	removing := strings.EqualFold(c.Target, "none")
	if !removing {
		data, readErr := os.ReadFile(c.Target)
		if readErr != nil {
			return fmt.Errorf("read the ceiling: %w", readErr)
		}
		if validateErr := systemauthority.ValidateCeiling(data); validateErr != nil {
			return fmt.Errorf("read the ceiling from %s: %w", c.Target, validateErr)
		}
		policy = data
	}
	if os.Geteuid() != 0 {
		return runPrivileged("system", "set-ceiling", c.Target)
	}
	store := systemauthority.DefaultCeilingStore()
	if removing {
		if err := store.Clear(); err != nil {
			return err
		}
		c.Logger.Success("This host no longer sets a ceiling.")
		return nil
	}
	if err := store.Store(policy); err != nil {
		return err
	}
	c.Logger.Success("This host now allows an application at most what %s describes.", c.Target)
	c.Logger.Info("An application already running keeps the policy it started with until it is restarted.")
	return nil
}

// reportTrust says who this host is willing to run software from. It is not
// privileged, because an application that will not install is something the
// person in front of the machine has to be able to explain.
func (c *SystemCmd) reportTrust() error {
	policy, err := systemauthority.DefaultTrustStore().Policy()
	if err != nil {
		return err
	}
	if policy.Empty() {
		c.Logger.Info("This host has decided nothing: any origin may be installed and any publisher may sign for it.")
		c.Logger.Info("cpak system set-trust FILE reads a policy that says which origins and which signers are allowed.")
		return nil
	}
	c.Logger.Info("This host allows software on these terms:")
	tools.PrintStructKeyVal(policy)
	c.Logger.Info("An origin, a publisher or a generation this policy does not allow is not enrolled, whatever it is signed with.")
	c.Logger.Info("cpak system set-trust none removes it.")
	return nil
}

// setTrust takes a file for the same reason the ceiling does: it is one
// decision about a whole policy, and setting it a field at a time would leave
// the host in states nobody chose.
func (c *SystemCmd) setTrust() error {
	if err := refuseSudoedStore(); err != nil {
		return err
	}
	if c.Target == "" {
		return fmt.Errorf("name the file the trust policy is read from, or none to remove it")
	}
	// The file is understood before anyone authenticates, so a mistyped path
	// costs a message rather than an administrator password.
	policy := trustpolicy.Policy{}
	removing := strings.EqualFold(c.Target, "none")
	if !removing {
		data, readErr := os.ReadFile(c.Target)
		if readErr != nil {
			return fmt.Errorf("read the trust policy: %w", readErr)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decodeErr := decoder.Decode(&policy); decodeErr != nil {
			return fmt.Errorf("read the trust policy from %s: %w", c.Target, decodeErr)
		}
		if validateErr := policy.Validate(); validateErr != nil {
			return fmt.Errorf("read the trust policy from %s: %w", c.Target, validateErr)
		}
	}
	if os.Geteuid() != 0 {
		return runPrivileged("system", "set-trust", c.Target)
	}
	store := systemauthority.DefaultTrustStore()
	if removing {
		if err := store.Clear(); err != nil {
			return err
		}
		c.Logger.Success("This host no longer decides who may publish what.")
		return nil
	}
	if err := store.Set(policy); err != nil {
		return err
	}
	c.Logger.Success("This host now allows software on the terms %s describes.", c.Target)
	c.Logger.Info("An application already enrolled keeps its anchor: the policy is applied the next time one is recorded.")
	return nil
}
