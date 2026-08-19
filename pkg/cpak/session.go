/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func (c *Cpak) Sessions(origin string) (types.Application, []types.Session, error) {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return types.Application{}, nil, err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, "", "", "", "")
	if err != nil || app.CpakId == "" {
		return types.Application{}, nil, fmt.Errorf("application %s is not installed", origin)
	}
	return app, append([]types.Session{}, app.ParsedSessions...), nil
}

func (c *Cpak) EnableSession(origin, id string) error {
	app, sessions, err := c.Sessions(origin)
	if err != nil {
		return err
	}
	session, err := findSession(sessions, id)
	if err != nil {
		return err
	}
	if !c.applicationPathExists(app, session.Entrypoint, true) {
		return fmt.Errorf("session entrypoint is missing: %s", session.Entrypoint)
	}
	return systemauthority.Register(systemauthority.Session{
		ID:          session.ID,
		Origin:      app.Origin,
		Name:        session.Name,
		Description: session.Description,
		Kind:        session.Kind,
	})
}

func (c *Cpak) DisableSession(id string) error {
	registered, err := systemauthority.DefaultRegistry().Load(id)
	if err != nil {
		return err
	}
	return systemauthority.Remove(id, registered.Origin)
}

func (c *Cpak) RunSession(id string, verbose bool) error {
	registry := systemauthority.DefaultRegistry()
	registered, err := registry.Load(id)
	if err != nil {
		return err
	}
	app, sessions, err := c.Sessions(registered.Origin)
	if err != nil {
		return err
	}
	session, err := findSession(sessions, registered.ID)
	if err != nil {
		return err
	}
	if session.Kind != registered.Kind {
		return fmt.Errorf("installed session kind does not match its registration")
	}
	if !c.applicationPathExists(app, session.Entrypoint, true) {
		return fmt.Errorf("session entrypoint is missing: %s", session.Entrypoint)
	}
	if err := c.prepareSocketListener(); err != nil {
		return err
	}
	return c.runApplicationInstance(app, asLaunched(session.Override), sessionInstance(session.ID), session.Entrypoint, verbose, false)
}

func findSession(sessions []types.Session, id string) (types.Session, error) {
	for _, session := range sessions {
		if session.ID == id {
			return session, nil
		}
	}
	return types.Session{}, fmt.Errorf("session %s is not declared by the package", id)
}

func disableRegisteredSessions(registry systemauthority.Registry, origin string, sessions []types.Session, remove func(string, string) error) error {
	seen := map[string]bool{}
	for _, session := range sessions {
		if seen[session.ID] {
			continue
		}
		seen[session.ID] = true
		registered, err := registry.Load(session.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect registered session %s: %w", session.ID, err)
		}
		if registered.Origin != origin {
			continue
		}
		if err := remove(session.ID, origin); err != nil {
			return err
		}
	}
	return nil
}

// A session instance is named after the session it is, and the launch gate
// reads that name back to know which recorded root the launch answers to. The
// two sides live here together so the spelling cannot drift: a session whose
// id the gate could not recover would be checked against the application's
// root, which is the disagreement this naming exists to avoid.
const sessionInstancePrefix = "session-"

func sessionInstance(id string) string {
	return sessionInstancePrefix + id
}

// sessionIDOfInstance answers the session an instance belongs to, and the empty
// string when the instance is not a session at all.
func sessionIDOfInstance(instance string) string {
	if !strings.HasPrefix(instance, sessionInstancePrefix) {
		return ""
	}
	return strings.TrimPrefix(instance, sessionInstancePrefix)
}
