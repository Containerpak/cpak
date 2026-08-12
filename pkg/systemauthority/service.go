/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systemauthority

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	BusName       = "it.cpak.SystemAuthority1"
	ObjectPath    = dbus.ObjectPath("/it/cpak/SystemAuthority1")
	InterfaceName = "it.cpak.SystemAuthority1"
)

type Service struct {
	Registry   Registry
	Authorizer Authorizer
}

func (s *Service) RegisterSession(sender dbus.Sender, id, origin, name, description, kind string) *dbus.Error {
	session := Session{ID: id, Origin: origin, Name: name, Description: description, Kind: kind}
	if err := session.Validate(); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionRegisterSession, map[string]string{
		"session-id":     session.ID,
		"package-origin": session.Origin,
		"session-kind":   session.Kind,
	}); err != nil {
		return denied(err)
	}
	if err := s.Registry.Register(session); err != nil {
		return failed(err)
	}
	return nil
}

func (s *Service) RemoveSession(sender dbus.Sender, id, origin string) *dbus.Error {
	if len(id) == 0 || len(id) > 96 || !sessionIDPattern.MatchString(id) {
		return invalidRequest(errors.New("invalid session identifier"))
	}
	if err := validateOrigin(origin); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionRemoveSession, map[string]string{
		"session-id":     id,
		"package-origin": origin,
	}); err != nil {
		return denied(err)
	}
	if err := s.Registry.Remove(id, origin); err != nil {
		return failed(err)
	}
	return nil
}

func Serve(ctx context.Context) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer connection.Close()
	service := &Service{
		Registry:   DefaultRegistry(),
		Authorizer: PolkitAuthorizer{Connection: connection},
	}
	if err := connection.Export(service, ObjectPath, InterfaceName); err != nil {
		return fmt.Errorf("export system authority: %w", err)
	}
	reply, err := connection.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("request system authority name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("system authority is already running")
	}
	<-ctx.Done()
	return nil
}

func invalidRequest(err error) *dbus.Error {
	return dbus.NewError("it.cpak.Error.InvalidRequest", []any{err.Error()})
}

func denied(err error) *dbus.Error {
	return dbus.NewError("it.cpak.Error.NotAuthorized", []any{err.Error()})
}

func failed(err error) *dbus.Error {
	return dbus.NewError("it.cpak.Error.Failed", []any{err.Error()})
}
