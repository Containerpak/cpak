/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemCmd struct {
	Action string `arg:"action" help:"Action: setup, remove, status, enforcement, set-enforcement, explain, register-session or remove-session"`
	Target string `arg:"target" help:"Enforcement level for set-enforcement, installed package origin for explain"`

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
		return systemauthority.Uninstall()
	case "enforcement":
		return c.reportEnforcement()
	case "set-enforcement":
		return c.setEnforcement()
	case "explain":
		return c.explain()
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
	return nil
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
	c.Logger.Info("An anchor records the installation as it stood when it was written. That is trust on first install: it says the application has not changed since, and it does not say the store holds what the publisher shipped.")
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
