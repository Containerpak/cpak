/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/types"
	"golang.org/x/sys/unix"
)

// The bus is one transport, not the service itself. Where no system bus runs
// the authority answers the same requests over a socket and identifies the
// caller from the credentials the kernel attaches to the connection, which is a
// stronger claim than a bus name because no broker sits in between.
const (
	DefaultSocketPath = "/run/cpak/authority.sock"
	socketDeadline    = 10 * time.Second

	// A signed enrolment carries the same bounded record the ledger writes.
	// Keeping the wire under that ceiling lets the socket carry the evidence
	// without giving a local caller an unbounded allocation in the authority.
	requestLimit = anchorSizeLimit
)

var errTransportUnavailable = errors.New("system authority transport is unavailable")
var errRootRequired = errors.New("system authority request requires root")

const socketCodeRootRequired = "root-required"

type socketRequest struct {
	Action      string            `json:"action"`
	ID          string            `json:"id"`
	Origin      string            `json:"origin"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Kind        string            `json:"kind,omitempty"`
	UID         uint32            `json:"uid,omitempty"`
	Anchor      *integrity.Anchor `json:"anchor,omitempty"`
	Policy      *types.Override   `json:"policy,omitempty"`
	Signature   *SignedState      `json:"signature,omitempty"`
	Level       string            `json:"level,omitempty"`
}

type socketResponse struct {
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

type socketService struct {
	Registry    Registry
	Anchors     AnchorLedger
	Enforcement EnforcementStore
	Authorize   func(*unix.Ucred) error
	mutations   *sync.Mutex
}

func ServeSocket(ctx context.Context, path string) error {
	service := socketService{
		Registry:    DefaultRegistry(),
		Anchors:     DefaultAnchorLedger(),
		Enforcement: DefaultEnforcementStore(),
	}
	return service.serve(ctx, path)
}

func (s socketService) serve(ctx context.Context, path string) error {
	if s.mutations == nil {
		s.mutations = &sync.Mutex{}
	}
	if path == "" {
		path = DefaultSocketPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create system authority directory: %w", err)
	}
	if err := removeStaleSocket(path); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for system authority: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	// Every local user may ask. What the answer is depends on the credentials
	// the kernel reports, never on who managed to reach the socket.
	if err := os.Chmod(path, 0666); err != nil {
		return fmt.Errorf("open system authority socket: %w", err)
	}
	var connections sync.WaitGroup
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	defer close(done)
	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				connections.Wait()
				return nil
			}
			return fmt.Errorf("accept system authority request: %w", acceptErr)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer connection.Close()
			s.answer(connection)
		}()
	}
}

func (s socketService) answer(connection *net.UnixConn) {
	_ = connection.SetDeadline(time.Now().Add(socketDeadline))
	message, err := decodeSocketRequest(connection)
	if err != nil {
		writeResponse(connection, errors.New("invalid system authority request"))
		return
	}
	writeResponse(connection, s.apply(connection, message))
}

func (s socketService) apply(connection *net.UnixConn, message socketRequest) error {
	credentials, err := peerCredentials(connection)
	if err != nil {
		return errors.New("system authority could not identify the caller")
	}
	authorize := s.Authorize
	authorizeRequest := func() error {
		if authorize != nil {
			return authorize(credentials)
		}
		return authorizeSocketRequest(credentials, message, s.Anchors)
	}
	if message.Action == anchorEnrolAction || message.Action == anchorForgetAction || message.Action == anchorClearAction {
		if s.mutations == nil {
			s.mutations = &sync.Mutex{}
		}
		s.mutations.Lock()
		defer s.mutations.Unlock()
		if err := authorizeRequest(); err != nil {
			return err
		}
		return applyAnchor(s.Anchors, message)
	}
	if err := authorizeRequest(); err != nil {
		return err
	}
	switch message.Action {
	case "register":
		session := Session{
			ID:          message.ID,
			Origin:      message.Origin,
			Name:        message.Name,
			Description: message.Description,
			Kind:        message.Kind,
		}
		if err := session.Validate(); err != nil {
			return err
		}
		return s.Registry.Register(session)
	case "remove":
		if len(message.ID) == 0 || len(message.ID) > 96 || !sessionIDPattern.MatchString(message.ID) {
			return errors.New("invalid session identifier")
		}
		if err := validateOrigin(message.Origin); err != nil {
			return err
		}
		return s.Registry.Remove(message.ID, message.Origin)
	case enforcementSetAction:
		return applyEnforcement(s.Enforcement, message)
	default:
		return errors.New("unsupported system authority action")
	}
}

// authorizeSocketRequest gives a caller only the two operations that affect
// its own ledger without widening access: recording a first, unchanged or
// narrower enrolment, and forgetting its own anchor. Everything else still
// re-enters cpak as root when no bus can ask Polkit.
func authorizeSocketRequest(credentials *unix.Ucred, message socketRequest, anchors AnchorLedger) error {
	if credentials == nil {
		return errors.New("system authority could not identify the caller")
	}
	if credentials.Uid == 0 {
		return nil
	}
	switch message.Action {
	case anchorEnrolAction:
		if message.Anchor == nil || message.Anchor.UID != credentials.Uid {
			return errRootRequired
		}
		enrolment, err := enrolmentFromSocket(message)
		if err != nil {
			return err
		}
		if _, err = enrolment.Signer(); err != nil && !errors.Is(err, ErrUnsigned) {
			return err
		}
		action, err := anchors.authorizationFor(enrolment)
		if err != nil {
			return err
		}
		if action == ActionEnrolAnchor {
			return nil
		}
	case anchorForgetAction:
		if message.UID == credentials.Uid {
			return nil
		}
	}
	return errRootRequired
}

func peerCredentials(connection *net.UnixConn) (*unix.Ucred, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credentials *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return credentials, socketErr
}

func writeResponse(connection *net.UnixConn, err error) {
	reply := socketResponse{}
	if err != nil {
		reply.Error = err.Error()
		if errors.Is(err, errRootRequired) {
			reply.Code = socketCodeRootRequired
		}
	}
	_ = json.NewEncoder(connection).Encode(reply)
}

func decodeSocketRequest(input io.Reader) (socketRequest, error) {
	frame, err := bufio.NewReader(io.LimitReader(input, requestLimit+1)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return socketRequest{}, err
	}
	if len(frame) > requestLimit {
		return socketRequest{}, errors.New("system authority request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	message := socketRequest{}
	if err = decoder.Decode(&message); err != nil {
		return socketRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return socketRequest{}, errors.New("system authority request contains multiple JSON values")
	}
	return message, nil
}

func requestOverSocket(path string, message socketRequest) error {
	connection, err := net.DialTimeout("unix", path, socketDeadline)
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(socketDeadline))
	if err := json.NewEncoder(connection).Encode(message); err != nil {
		return fmt.Errorf("send system authority request: %w", err)
	}
	var reply socketResponse
	decoder := json.NewDecoder(io.LimitReader(connection, requestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return fmt.Errorf("read system authority reply: %w", err)
	}
	if reply.Code == socketCodeRootRequired {
		return errRootRequired
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	return nil
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect system authority socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("system authority socket path is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove system authority socket: %w", err)
	}
	return nil
}
