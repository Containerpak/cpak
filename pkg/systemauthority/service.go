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
	Registry    Registry
	Anchors     AnchorLedger
	Enforcement EnforcementStore
	Trust       TrustStore
	Authorizer  Authorizer

	// CallerUID names the account behind a bus name. An enrolment is only the
	// ordinary course of installing software for the account it is about, so
	// the authority has to know who is asking before it decides how hard to
	// ask back.
	CallerUID func(dbus.Sender) (uint32, error)
}

func (s *Service) RegisterSession(sender dbus.Sender, id, origin, name, description, kind string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
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
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
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

// EnrolAnchor takes the record apart on the wire because the bus carries plain
// values, and puts it back together here so the ledger sees the same record a
// local enrolment would hand it.
func (s *Service) EnrolAnchor(sender dbus.Sender, abi int32, uid uint32, origin string, generation uint64, imageDigest, manifestDigest, packageRoot, policyRoot, launchRoot, policy string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	decoded, err := decodePolicy(policy)
	if err != nil {
		return invalidRequest(err)
	}
	enrolment := Enrolment{
		Anchor: integrity.Anchor{
			ABI:            int(abi),
			UID:            uid,
			Origin:         origin,
			Generation:     generation,
			ImageDigest:    imageDigest,
			ManifestDigest: manifestDigest,
			PackageRoot:    packageRoot,
			PolicyRoot:     policyRoot,
			LaunchRoot:     launchRoot,
		},
		Policy: decoded,
	}
	if err := validateEnrolment(enrolment); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	action, err := s.enrolmentAction(sender, enrolment)
	if err != nil {
		return failed(err)
	}
	if err := s.Authorizer.Authorize(sender, action, map[string]string{
		"package-origin": enrolment.Origin,
		"target-uid":     strconv.FormatUint(uint64(enrolment.UID), 10),
		"generation":     strconv.FormatUint(enrolment.Generation, 10),
	}); err != nil {
		return denied(err)
	}
	if err := s.Anchors.Record(enrolment); err != nil {
		return failed(err)
	}
	return nil
}

// enrolmentAction decides how hard to ask. Recording an anchor for somebody
// else says what their applications are, which is never the ordinary course of
// installing one's own software, and a caller the bus cannot name is nobody's
// ordinary course either.
func (s *Service) enrolmentAction(sender dbus.Sender, enrolment Enrolment) (string, error) {
	if s.CallerUID == nil {
		return ActionWidenAnchor, nil
	}
	uid, err := s.CallerUID(sender)
	if err != nil || uid != enrolment.UID {
		return ActionWidenAnchor, nil
	}
	return s.Anchors.authorizationFor(enrolment)
}

// SetEnforcement turns refusals on for every account on the host, so it is the
// owner of the machine's decision and it is never taken from anything a caller
// carries with it.
func (s *Service) SetEnforcement(sender dbus.Sender, level string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	wanted := EnforcementLevel(level)
	if !wanted.valid() {
		return invalidRequest(errors.New("invalid enforcement level"))
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, ActionSetEnforcement, map[string]string{
		"enforcement-level": level,
	}); err != nil {
		return denied(err)
	}
	if err := s.Enforcement.Set(wanted); err != nil {
		return failed(err)
	}
	return nil
}

// busCallerUID asks the bus who owns a name. The bus is the only one that can
// answer it: the caller must never be asked, because the answer decides whether
// the caller is asked for a password.
func busCallerUID(connection *dbus.Conn) func(dbus.Sender) (uint32, error) {
	return func(sender dbus.Sender) (uint32, error) {
		if connection == nil || sender == "" {
			return 0, errors.New("authorization subject is unavailable")
		}
		var uid uint32
		call := connection.BusObject().Call("org.freedesktop.DBus.GetConnectionUnixUser", 0, string(sender))
		if call.Err != nil {
			return 0, fmt.Errorf("identify the caller: %w", call.Err)
		}
		if err := call.Store(&uid); err != nil {
			return 0, fmt.Errorf("identify the caller: %w", err)
		}
		return uid, nil
	}
}

// forgetAction decides how hard to ask, the way enrolmentAction does for
// recording one.
//
// Forgetting used to be one question for everybody, on the reasoning that it
// takes a permission away rather than granting one. That reasoning is wrong.
// The anchor is the only record the rules against going backwards are derived
// from, so removing it does not leave an application with less: it leaves the
// next install of an older release looking like a first enrolment, with nothing
// to compare it against. Doing that to another account is not the ordinary
// course of managing one's own software, and the caller does not get to say
// whose anchor it is.
func (s *Service) forgetAction(sender dbus.Sender, uid uint32) string {
	if s.CallerUID == nil {
		return ActionForgetAnchorOther
	}
	caller, err := s.CallerUID(sender)
	if err != nil || caller != uid {
		return ActionForgetAnchorOther
	}
	return ActionForgetAnchor
}

func (s *Service) ForgetAnchor(sender dbus.Sender, uid uint32, origin string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	if err := validateOrigin(origin); err != nil {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	if err := s.Authorizer.Authorize(sender, s.forgetAction(sender, uid), map[string]string{
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
		Registry:    DefaultRegistry(),
		Anchors:     DefaultAnchorLedger(),
		Enforcement: DefaultEnforcementStore(),
		Trust:       DefaultTrustStore(),
		Authorizer:  PolkitAuthorizer{Connection: connection},
		CallerUID:   busCallerUID(connection),
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
