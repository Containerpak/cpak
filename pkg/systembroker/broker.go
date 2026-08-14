/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxRequestSize = 16 << 10
)

type Options struct {
	SocketPath            string
	Token                 string
	AllowNotify           bool
	AllowOpenURI          bool
	AllowHostApplications bool
	OpenURICommand        string
	Applications          map[string]string
	RuntimeDirectory      string
	CommandTimeout        time.Duration
	AuthorizePeer         func(*net.UnixConn) error
	ContainerOwner        string
	ContainerCapabilities map[string]bool
	ContainerPaths        []ContainerPathGrant
	Notify                func(context.Context, NotificationRequest) error
	LaunchApplication     func(context.Context, string, []string, []string) error
	Containers            func(context.Context, string, map[string]bool, []ContainerPathGrant, ContainerRequest, io.Writer, io.Writer) (int, error)
}

func (o Options) validate() error {
	if o.SocketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if len(o.Token) < 32 {
		return errors.New("system broker token is too short")
	}
	if !o.AllowNotify && !o.AllowOpenURI && !o.AllowHostApplications && len(o.ContainerCapabilities) == 0 {
		return errors.New("system broker has no enabled operations")
	}
	if o.AllowHostApplications {
		if err := validateRuntimeDirectory(o.RuntimeDirectory); err != nil {
			return err
		}
		if o.Applications == nil {
			return errors.New("system broker application catalog is required")
		}
	}
	if len(o.ContainerCapabilities) > 0 && o.ContainerOwner == "" {
		return errors.New("system broker container owner is required")
	}
	if len(o.ContainerOwner) > 512 || strings.ContainsAny(o.ContainerOwner, "\x00\r\n") {
		return errors.New("system broker container owner is invalid")
	}
	for capability := range o.ContainerCapabilities {
		if capability != "read" && capability != "manage-owned" && capability != "exec-owned" {
			return fmt.Errorf("unsupported container capability: %s", capability)
		}
	}
	for _, path := range o.ContainerPaths {
		if !filepath.IsAbs(path.Path) || filepath.Clean(path.Path) != path.Path {
			return errors.New("system broker container path is invalid")
		}
	}
	return nil
}

func (o Options) openURICommand() string {
	if o.OpenURICommand != "" {
		return o.OpenURICommand
	}
	return "xdg-open"
}

func (o Options) commandTimeout() time.Duration {
	if o.CommandTimeout > 0 {
		return o.CommandTimeout
	}
	return 10 * time.Second
}

func (o Options) authorize(connection *net.UnixConn) error {
	if o.AuthorizePeer != nil {
		return o.AuthorizePeer(connection)
	}
	return authorizePeer(connection)
}

func (o Options) notify(ctx context.Context, request NotificationRequest) error {
	if o.Notify != nil {
		return o.Notify(ctx, request)
	}
	return sendNotification(ctx, request)
}

func Serve(ctx context.Context, options Options) error {
	if err := options.validate(); err != nil {
		return err
	}
	return serve(ctx, options.SocketPath, options.authorize, func(Request) (Options, error) {
		return options, nil
	})
}

func handle(connection *net.UnixConn, options Options) {
	handleResolved(connection, options.authorize, func(Request) (Options, error) {
		return options, nil
	})
}

func handleResolved(connection *net.UnixConn, authorize func(*net.UnixConn) error, resolve func(Request) (Options, error)) {
	_ = connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	writer := newFrameWriter(connection)
	if err := authorize(connection); err != nil {
		writer.fail("system broker denied the caller")
		return
	}
	request := Request{}
	if err := json.NewDecoder(io.LimitReader(connection, maxRequestSize)).Decode(&request); err != nil {
		writer.fail("invalid system broker request")
		return
	}
	options, err := resolve(request)
	if err != nil {
		writer.fail("system broker denied the request")
		return
	}
	if err = options.validate(); err != nil {
		writer.fail("system broker denied the request")
		return
	}
	if err := authorizeRequest(request, options.Token); err != nil {
		writer.fail("system broker denied the request")
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var buffer [1]byte
		_, _ = connection.Read(buffer[:])
		cancel()
	}()
	code, err := execute(ctx, request, options, writer)
	if err != nil {
		writer.fail(err.Error())
		return
	}
	writer.exit(code)
}

func authorizeRequest(request Request, token string) error {
	if request.Version != ProtocolVersion {
		return errors.New("unsupported system broker protocol")
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(token)) != 1 {
		return errors.New("invalid system broker token")
	}
	if request.Action != ActionNotify && request.Action != ActionOpenURI && request.Action != ActionLaunchApplication && request.Action != ActionContainers {
		return errors.New("unsupported system broker operation")
	}
	return nil
}

func execute(ctx context.Context, request Request, options Options, writer *frameWriter) (int, error) {
	switch request.Action {
	case ActionNotify:
		if !options.AllowNotify {
			return 0, errors.New("desktop notifications are not permitted")
		}
		notification := NotificationRequest{}
		if err := decodePayload(request.Payload, &notification); err != nil {
			return 0, err
		}
		if err := validateNotification(notification); err != nil {
			return 0, err
		}
		commandCtx, cancel := context.WithTimeout(ctx, options.commandTimeout())
		defer cancel()
		if err := options.notify(commandCtx, notification); err != nil {
			if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
				return 0, errors.New("system integration backend timed out: notification service")
			}
			return 0, fmt.Errorf("system integration backend failed: notification service: %w", err)
		}
		return 0, nil
	case ActionOpenURI:
		if !options.AllowOpenURI {
			return 0, errors.New("opening URIs is not permitted")
		}
		openURI := OpenURIRequest{}
		if err := decodePayload(request.Payload, &openURI); err != nil {
			return 0, err
		}
		if err := validateOpenURI(openURI); err != nil {
			return 0, err
		}
		path, err := exec.LookPath(options.openURICommand())
		if err != nil {
			return 0, fmt.Errorf("system integration backend is unavailable: %s", options.openURICommand())
		}
		command := exec.Command(path, openURI.URI)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return 0, fmt.Errorf("system integration backend failed: %s", options.openURICommand())
		}
		if err := command.Process.Release(); err != nil {
			return 0, fmt.Errorf("release system integration backend: %s", options.openURICommand())
		}
		return 0, nil
	case ActionLaunchApplication:
		if !options.AllowHostApplications {
			return 0, errors.New("host applications are not permitted")
		}
		launch := LaunchApplicationRequest{}
		if err := decodePayload(request.Payload, &launch); err != nil {
			return 0, err
		}
		desktopEntry, args, err := validateApplicationRequest(launch, options.Applications)
		if err != nil {
			return 0, err
		}
		environment, err := applicationEnvironment(launch.Environment, options.RuntimeDirectory)
		if err != nil {
			return 0, err
		}
		commandCtx, cancel := context.WithTimeout(ctx, options.commandTimeout())
		defer cancel()
		if options.LaunchApplication != nil {
			return 0, options.LaunchApplication(commandCtx, desktopEntry, args, environment)
		}
		path, err := exec.LookPath("gio")
		if err != nil {
			return 0, errors.New("host application backend is unavailable: gio")
		}
		command := exec.Command(path, append([]string{"launch", desktopEntry}, args...)...)
		command.Env = mergeEnvironment(os.Environ(), environment)
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return 0, errors.New("host application backend failed: gio")
		}
		if err := command.Process.Release(); err != nil {
			return 0, errors.New("release host application backend: gio")
		}
		return 0, nil
	case ActionContainers:
		if len(options.ContainerCapabilities) == 0 {
			return 0, errors.New("host container actions are not permitted")
		}
		container := ContainerRequest{}
		if err := decodePayload(request.Payload, &container); err != nil {
			return 0, err
		}
		if options.Containers != nil {
			return options.Containers(ctx, options.ContainerOwner, options.ContainerCapabilities, options.ContainerPaths, container, writer.stdout(), writer.stderr())
		}
		return executeContainer(ctx, options.ContainerOwner, options.ContainerCapabilities, options.ContainerPaths, container, writer.stdout(), writer.stderr())
	}
	return 0, errors.New("unsupported system broker operation")
}

type frameWriter struct {
	encoder *json.Encoder
	mu      sync.Mutex
}

type streamWriter struct {
	frames *frameWriter
	typeID string
}

func newFrameWriter(output io.Writer) *frameWriter {
	return &frameWriter{encoder: json.NewEncoder(output)}
}

func (w *frameWriter) write(frame Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.encoder.Encode(frame)
}

func (w *frameWriter) fail(message string) {
	_ = w.write(Frame{Type: FrameError, Error: message})
}

func (w *frameWriter) exit(code int) {
	_ = w.write(Frame{Type: FrameExit, ExitCode: code})
}

func (w *frameWriter) stdout() io.Writer {
	return streamWriter{frames: w, typeID: FrameStdout}
}

func (w *frameWriter) stderr() io.Writer {
	return streamWriter{frames: w, typeID: FrameStderr}
}

func (w streamWriter) Write(data []byte) (int, error) {
	copy := append([]byte{}, data...)
	if err := w.frames.write(Frame{Type: w.typeID, Data: copy}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func decodePayload(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid system broker payload")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid system broker payload")
	}
	return nil
}

var displayPattern = regexp.MustCompile(`^:[0-9]+(?:\.[0-9]+)?$`)
var waylandDisplayPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func validateApplicationRequest(request LaunchApplicationRequest, applications map[string]string) (string, []string, error) {
	if len(request.URIs) > 32 || len(request.ApplicationToken) != 64 {
		return "", nil, errors.New("invalid host application request")
	}
	for _, character := range request.ApplicationToken {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", nil, errors.New("invalid host application request")
		}
	}
	desktopEntry := applications[request.ApplicationToken]
	if desktopEntry == "" || !filepath.IsAbs(desktopEntry) || filepath.Ext(desktopEntry) != ".desktop" {
		return "", nil, errors.New("host application is not in the catalog")
	}
	for _, arg := range request.URIs {
		if err := validateApplicationURI(arg); err != nil {
			return "", nil, errors.New("invalid host application request")
		}
	}
	return desktopEntry, request.URIs, nil
}

func validateApplicationURI(value string) error {
	if len(value) > 4096 || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "-") {
		return errors.New("invalid application URI")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid application URI")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file", "http", "https", "mailto":
		return nil
	default:
		return errors.New("application URI scheme is not permitted")
	}
}

func applicationEnvironment(request map[string]string, runtimeDirectory string) ([]string, error) {
	environment := []string{}
	if display := request["WAYLAND_DISPLAY"]; display != "" {
		if !waylandDisplayPattern.MatchString(display) {
			return nil, errors.New("invalid Wayland display")
		}
		socket := filepath.Join(runtimeDirectory, display)
		info, err := os.Stat(socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("Wayland display is unavailable")
		}
		environment = append(environment, "WAYLAND_DISPLAY="+socket)
	}
	if display := request["DISPLAY"]; display != "" {
		if !displayPattern.MatchString(display) {
			return nil, errors.New("invalid X11 display")
		}
		environment = append(environment, "DISPLAY="+display)
	}
	if token := request["XDG_ACTIVATION_TOKEN"]; token != "" {
		if len(token) > 4096 || strings.ContainsRune(token, '\x00') {
			return nil, errors.New("invalid activation token")
		}
		environment = append(environment, "XDG_ACTIVATION_TOKEN="+token)
	}
	if len(environment) == 0 {
		return nil, errors.New("host application display is unavailable")
	}
	return environment, nil
}

func LoadApplicationCatalog(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read application catalog: %w", err)
	}
	if info.Size() > 4<<20 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("application catalog is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read application catalog: %w", err)
	}
	catalog := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode application catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("application catalog contains multiple JSON values")
	}
	for token, desktopEntry := range catalog {
		request := LaunchApplicationRequest{ApplicationToken: token}
		if _, _, err := validateApplicationRequest(request, map[string]string{token: desktopEntry}); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func validateRuntimeDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("system broker desktop runtime must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("system broker desktop runtime must be a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("system broker desktop runtime has an unexpected owner")
	}
	return nil
}

func mergeEnvironment(base, overrides []string) []string {
	names := make(map[string]bool, len(overrides))
	for _, value := range overrides {
		if name, _, found := strings.Cut(value, "="); found {
			names[name] = true
		}
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, value := range base {
		name, _, found := strings.Cut(value, "=")
		if found && names[name] {
			continue
		}
		result = append(result, value)
	}
	return append(result, overrides...)
}

func validateOpenURI(request OpenURIRequest) error {
	if len(request.URI) > 4096 || strings.ContainsRune(request.URI, '\x00') {
		return errors.New("invalid URI request")
	}
	parsed, err := url.ParseRequestURI(request.URI)
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid URI request")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
		return nil
	default:
		return errors.New("URI scheme is not permitted")
	}
}

func authorizePeer(connection *net.UnixConn) error {
	var credentials *unix.Ucred
	var controlErr error
	raw, err := connection.SyscallConn()
	if err != nil {
		return err
	}
	err = raw.Control(func(fd uintptr) {
		credentials, controlErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return err
	}
	if controlErr != nil {
		return controlErr
	}
	if credentials == nil || credentials.Uid != uint32(os.Getuid()) {
		return errors.New("unexpected system broker peer")
	}
	return nil
}

func removeSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect system broker socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("system broker path is not a socket")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("system broker socket has an unexpected owner")
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return errors.New("system broker socket is already active")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale system broker socket: %w", err)
	}
	return nil
}
