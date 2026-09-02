/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import "testing"

func TestDBusPolicyAllowsOnlyExactCalls(t *testing.T) {
	policy := DBusPolicy{
		Talk: []DBusCallGrant{{
			Name:      "org.example.Editor",
			Path:      "/org/example/Editor",
			Interface: "org.example.Editor.Documents",
			Members:   []string{"Open"},
		}},
		Own: []string{"org.example.Editor.Instance"},
	}
	if err := ValidateDBusPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsCall("org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Open") {
		t.Fatal("the declared call was refused")
	}
	if policy.AllowsCall("org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Delete") {
		t.Fatal("an undeclared member was allowed")
	}
	if !policy.AllowsOwn("org.example.Editor.Instance") || policy.AllowsOwn("org.freedesktop.systemd1") {
		t.Fatal("the own-name policy was not exact")
	}
}

func TestDBusPolicyAllowsNamesUnderDeclaredNamespace(t *testing.T) {
	policy := DBusPolicy{Own: []string{"com.steampowered.PressureVessel.*"}}
	if err := ValidateDBusPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if !policy.AllowsOwn("com.steampowered.PressureVessel.LaunchAlongsideSteam.Instance294") {
		t.Fatal("a name under the declared namespace was refused")
	}
	for _, name := range []string{
		"com.steampowered.PressureVessel",
		"com.steampowered.PressureVesselHelper.Instance294",
		"com.steampowered.Other.Instance294",
	} {
		if policy.AllowsOwn(name) {
			t.Fatalf("the namespace rule allowed %s", name)
		}
	}
	if (DBusPolicy{Own: []string{"*"}}).AllowsOwn("com.steampowered.PressureVessel.Instance294") {
		t.Fatal("an invalid rule widened the session bus policy")
	}
}

func TestDBusPolicyRejectsAmbiguousRules(t *testing.T) {
	rule := DBusCallGrant{
		Name:      "org.example.Editor",
		Path:      "/org/example/Editor",
		Interface: "org.example.Editor.Documents",
		Members:   []string{"Open"},
	}
	for _, policy := range []DBusPolicy{
		{Talk: []DBusCallGrant{rule, rule}},
		{Talk: []DBusCallGrant{{Name: ":1.20", Path: rule.Path, Interface: rule.Interface, Members: rule.Members}}},
		{Talk: []DBusCallGrant{{Name: rule.Name, Path: "/org/example/../Host", Interface: rule.Interface, Members: rule.Members}}},
		{Talk: []DBusCallGrant{{Name: rule.Name, Path: rule.Path, Interface: rule.Interface, Members: []string{"Open", "Open"}}}},
		{Own: []string{"org.*"}},
		{Own: []string{"org.example.*.Helper"}},
		{Own: []string{"org.example.**"}},
	} {
		if err := ValidateDBusPolicy(policy); err == nil {
			t.Fatalf("invalid session bus policy was accepted: %+v", policy)
		}
	}
}

func TestDBusPolicyIntersectionNarrowsOwnedNamespaces(t *testing.T) {
	wide := DBusPolicy{Own: []string{"org.example.*"}}
	narrow := DBusPolicy{Own: []string{"org.example.Editor.*", "org.example.Player"}}
	shared := IntersectDBusPolicies(wide, narrow)

	if !shared.AllowsOwn("org.example.Editor.Instance7") || !shared.AllowsOwn("org.example.Player") {
		t.Fatalf("session bus intersection lost a narrower rule: %+v", shared)
	}
	if shared.AllowsOwn("org.example.Other") {
		t.Fatalf("session bus intersection widened its inputs: %+v", shared)
	}
	if !DBusPolicyRestricts(wide, shared) || DBusPolicyRestricts(narrow, wide) {
		t.Fatal("session bus namespace restriction was inverted")
	}
}

func TestDBusPolicyIntersectionCannotWiden(t *testing.T) {
	parent := DBusPolicy{
		Talk: []DBusCallGrant{{Name: "org.example.Editor", Path: "/org/example/Editor", Interface: "org.example.Editor.Documents", Members: []string{"Open", "Save"}}},
		Own:  []string{"org.example.Editor.Instance"},
	}
	child := DBusPolicy{
		Talk: []DBusCallGrant{{Name: "org.example.Editor", Path: "/org/example/Editor", Interface: "org.example.Editor.Documents", Members: []string{"Save", "Delete"}}},
		Own:  []string{"org.example.Editor.Instance", "org.example.Editor.Helper"},
	}
	shared := IntersectDBusPolicies(parent, child)
	if !shared.AllowsCall("org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Save") || shared.AllowsCall("org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Open") || shared.AllowsCall("org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Delete") {
		t.Fatalf("session bus intersection: %+v", shared)
	}
	if !DBusPolicyRestricts(parent, shared) || !DBusPolicyRestricts(child, shared) {
		t.Fatal("the intersection widened one of its inputs")
	}
}
