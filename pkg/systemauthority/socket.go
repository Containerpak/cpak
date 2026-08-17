/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
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

	"golang.org/x/sys/unix"
)

// The bus is one transport, not the service itself. Where no system bus runs
// the authority answers the same requests over a socket and identifies the
// caller from the credentials the kernel attaches to the connection, which is a
// stronger claim than a bus name because no broker sits in between.
const (
	DefaultSocketPath = "/run/cpak/authority.sock"
	socketDeadline    = 10 * time.Second
	requestLimit      = 8 << 10
)

var errTransportUnavailable = errors.New("system authority is not reachable")

type socketRequest struct {
	Action      string `json:"action"`
	ID          string `json:"id"`
	Origin      string `json:"origin"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

type socketResponse struct {
	Error string `json:"error,omitempty"`
}

type socketService struct {
	Registry  Registry
	Authorize func(*unix.Ucred) error
}

func ServeSocket(ctx context.Context, path string) error {
	service := socketService{Registry: DefaultRegistry(), Authorize: authorizePeerCredentials}
	return service.serve(ctx, path)
}

func (s socketService) serve(ctx context.Context, path string) error {
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
	var message socketRequest
	if err := json.NewDecoder(io.LimitReader(connection, requestLimit)).Decode(&message); err != nil {
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
	if authorize == nil {
		authorize = authorizePeerCredentials
	}
	if err := authorize(credentials); err != nil {
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
	default:
		return errors.New("unsupported system authority action")
	}
}

// authorizePeerCredentials keeps the socket at the guarantee the bus gives
// through polkit: an unprivileged caller is only ever authorized interactively,
// which needs the bus, so on this transport the request must come from root.
func authorizePeerCredentials(credentials *unix.Ucred) error {
	if credentials == nil {
		return errors.New("system authority could not identify the caller")
	}
	if credentials.Uid != 0 {
		return errors.New("changing login sessions over the authority socket requires root")
	}
	return nil
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
	}
	_ = json.NewEncoder(connection).Encode(reply)
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
	if err := json.NewDecoder(io.LimitReader(connection, requestLimit)).Decode(&reply); err != nil {
		return fmt.Errorf("read system authority reply: %w", err)
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
