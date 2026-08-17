/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/integrity"
)

const (
	BusName       = "it.cpak.SystemAuthority1"
	ObjectPath    = dbus.ObjectPath("/it/cpak/SystemAuthority1")
	InterfaceName = "it.cpak.SystemAuthority1"
)

type Service struct {
	Registry   Registry
	Anchors    AnchorLedger
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

// EnrollAnchor takes the anchor apart on the wire because the bus carries plain
// values, and puts it back together here so the ledger sees the same record a
// local enrolment would hand it.
func (s *Service) EnrollAnchor(sender dbus.Sender, abi int32, uid uint32, origin string, generation uint64, packageRoot, policyRoot, launchRoot string) *dbus.Error {
	anchor := integrity.Anchor{
		ABI:         int(abi),
		UID:         uid,
		Origin:      origin,
		Generation:  generation,
		PackageRoot: packageRoot,
		PolicyRoot:  policyRoot,
		LaunchRoot:  launchRoot,
	}
	if err := validateAnchor(anchor); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionEnrollAnchor, map[string]string{
		"package-origin": anchor.Origin,
		"target-uid":     strconv.FormatUint(uint64(anchor.UID), 10),
		"generation":     strconv.FormatUint(anchor.Generation, 10),
	}); err != nil {
		return denied(err)
	}
	if err := s.Anchors.Store(anchor); err != nil {
		return failed(err)
	}
	return nil
}

func (s *Service) ForgetAnchor(sender dbus.Sender, uid uint32, origin string) *dbus.Error {
	if err := validateOrigin(origin); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionForgetAnchor, map[string]string{
		"package-origin": origin,
		"target-uid":     strconv.FormatUint(uint64(uid), 10),
	}); err != nil {
		return denied(err)
	}
	if err := s.Anchors.Forget(uid, origin); err != nil {
		return failed(err)
	}
	return nil
}

// Serve answers on every transport the host offers. The socket is always
// available, so a machine without a system bus keeps a working authority
// instead of none, and the bus is taken as an extra when it is there because it
// is what carries an interactive polkit authorization.
func Serve(ctx context.Context, socketPath string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failure := make(chan error, 1)
	var serving sync.WaitGroup
	serving.Add(1)
	go func() {
		defer serving.Done()
		if err := ServeSocket(ctx, socketPath); err != nil {
			failure <- err
			cancel()
		}
	}()
	busErr := serveBus(ctx)
	// A missing bus is not a reason to stop: the socket keeps serving until the
	// caller asks the authority to shut down.
	if !errors.Is(busErr, errTransportUnavailable) {
		cancel()
	}
	serving.Wait()
	select {
	case err := <-failure:
		return err
	default:
	}
	if busErr != nil && !errors.Is(busErr, errTransportUnavailable) {
		return busErr
	}
	return nil
}

func serveBus(ctx context.Context) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	service := &Service{
		Registry:   DefaultRegistry(),
		Anchors:    DefaultAnchorLedger(),
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
