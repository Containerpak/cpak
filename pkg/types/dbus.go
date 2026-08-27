/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	dbusNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z_][A-Za-z0-9_-]*)+$`)
	dbusInterfacePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	dbusMemberPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	dbusPathPattern      = regexp.MustCompile(`^/(?:[A-Za-z0-9_]+(?:/[A-Za-z0-9_]+)*)?$`)
)

type DBusPolicy struct {
	Talk []DBusCallGrant `json:"talk,omitempty" jsonschema:"maxItems=64,description=Exact session bus calls the application may make"`
	Own  []string        `json:"own,omitempty" jsonschema:"maxItems=32,uniqueItems=true,description=Session bus names the application may own"`
}

type DBusCallGrant struct {
	Name      string   `json:"name" jsonschema:"maxLength=255,description=Well-known destination name"`
	Path      string   `json:"path" jsonschema:"maxLength=255,description=Exact object path"`
	Interface string   `json:"interface" jsonschema:"maxLength=255,description=Exact interface name"`
	Members   []string `json:"members" jsonschema:"minItems=1,maxItems=64,uniqueItems=true,description=Method names allowed on the interface"`
}

func (p DBusPolicy) Enabled() bool {
	return len(p.Talk) > 0 || len(p.Own) > 0
}

func ValidateDBusPolicy(policy DBusPolicy) error {
	if len(policy.Talk) > 64 || len(policy.Own) > 32 {
		return errors.New("session bus policy is too large")
	}
	owned := make(map[string]bool, len(policy.Own))
	for _, name := range policy.Own {
		if !validDBusName(name) {
			return fmt.Errorf("invalid session bus name: %q", name)
		}
		if owned[name] {
			return fmt.Errorf("session bus name is declared more than once: %s", name)
		}
		owned[name] = true
	}
	rules := make(map[string]bool, len(policy.Talk))
	for _, rule := range policy.Talk {
		if !validDBusName(rule.Name) {
			return fmt.Errorf("invalid session bus destination: %q", rule.Name)
		}
		if len(rule.Path) > 255 || !dbusPathPattern.MatchString(rule.Path) {
			return fmt.Errorf("invalid session bus object path: %q", rule.Path)
		}
		if len(rule.Interface) > 255 || !dbusInterfacePattern.MatchString(rule.Interface) {
			return fmt.Errorf("invalid session bus interface: %q", rule.Interface)
		}
		if len(rule.Members) == 0 || len(rule.Members) > 64 {
			return fmt.Errorf("session bus rule for %s has an invalid member count", rule.Name)
		}
		members := make(map[string]bool, len(rule.Members))
		for _, member := range rule.Members {
			if len(member) > 255 || !dbusMemberPattern.MatchString(member) {
				return fmt.Errorf("invalid session bus member: %q", member)
			}
			if members[member] {
				return fmt.Errorf("session bus member is declared more than once: %s", member)
			}
			members[member] = true
		}
		key := rule.Name + "\x00" + rule.Path + "\x00" + rule.Interface
		if rules[key] {
			return fmt.Errorf("session bus rule is declared more than once for %s %s %s", rule.Name, rule.Path, rule.Interface)
		}
		rules[key] = true
	}
	return nil
}

func validDBusName(name string) bool {
	return len(name) <= 255 && dbusNamePattern.MatchString(name)
}

func (p DBusPolicy) AllowsCall(name, path, interfaceName, member string) bool {
	for _, rule := range p.Talk {
		if rule.Name != name || rule.Path != path || rule.Interface != interfaceName {
			continue
		}
		for _, allowed := range rule.Members {
			if allowed == member {
				return true
			}
		}
	}
	return false
}

func (p DBusPolicy) AllowsOwn(name string) bool {
	for _, allowed := range p.Own {
		if allowed == name {
			return true
		}
	}
	return false
}

func DBusPolicyRestricts(current, candidate DBusPolicy) bool {
	for _, name := range candidate.Own {
		if !current.AllowsOwn(name) {
			return false
		}
	}
	for _, rule := range candidate.Talk {
		for _, member := range rule.Members {
			if !current.AllowsCall(rule.Name, rule.Path, rule.Interface, member) {
				return false
			}
		}
	}
	return true
}

func IntersectDBusPolicies(left, right DBusPolicy) DBusPolicy {
	result := DBusPolicy{}
	for _, name := range left.Own {
		if right.AllowsOwn(name) {
			result.Own = append(result.Own, name)
		}
	}
	for _, rule := range left.Talk {
		shared := DBusCallGrant{Name: rule.Name, Path: rule.Path, Interface: rule.Interface}
		for _, member := range rule.Members {
			if right.AllowsCall(rule.Name, rule.Path, rule.Interface, member) {
				shared.Members = append(shared.Members, member)
			}
		}
		if len(shared.Members) > 0 {
			result.Talk = append(result.Talk, shared)
		}
	}
	return CanonicalDBusPolicy(result)
}

func CanonicalDBusPolicy(policy DBusPolicy) DBusPolicy {
	result := DBusPolicy{Own: append([]string{}, policy.Own...)}
	sort.Strings(result.Own)
	result.Talk = make([]DBusCallGrant, len(policy.Talk))
	for index, rule := range policy.Talk {
		rule.Members = append([]string{}, rule.Members...)
		sort.Strings(rule.Members)
		result.Talk[index] = rule
	}
	sort.Slice(result.Talk, func(left, right int) bool {
		leftKey := result.Talk[left].Name + "\x00" + result.Talk[left].Path + "\x00" + result.Talk[left].Interface
		rightKey := result.Talk[right].Name + "\x00" + result.Talk[right].Path + "\x00" + result.Talk[right].Interface
		return strings.Compare(leftKey, rightKey) < 0
	})
	return result
}
