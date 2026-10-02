/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	desktopCallbackLifetime = 12 * time.Minute
	maxDesktopCallbackURI   = 128 << 10
	maxDesktopCallbacks     = 256
)

type desktopCallbackRecord struct {
	Origin   string `json:"origin"`
	Instance string `json:"instance"`
	Expires  int64  `json:"expires"`
}

var (
	desktopCallbackNow        = time.Now
	desktopCallbackRegisterMu sync.Mutex
)

// IsDesktopCallback reports whether a URI carries a correlated OAuth result.
func IsDesktopCallback(value string) bool {
	_, ok := responseCallbackKey(value)
	return ok
}

// RegisterDesktopCallback remembers which application instance opened an
// OAuth request. Requests without a custom return URI are ignored.
func RegisterDesktopCallback(directory, authorizationURI, origin, instance string) error {
	key, ok := authorizationCallbackKey(authorizationURI)
	if !ok || directory == "" || origin == "" {
		return nil
	}
	if err := validateDesktopCallbackTarget(origin, instance); err != nil {
		return err
	}
	if err := prepareDesktopCallbackDirectory(directory); err != nil {
		return err
	}
	desktopCallbackRegisterMu.Lock()
	defer desktopCallbackRegisterMu.Unlock()
	now := desktopCallbackNow()
	if cleanupDesktopCallbacks(directory, now) >= maxDesktopCallbacks {
		return errors.New("too many pending desktop callbacks")
	}
	record := desktopCallbackRecord{
		Origin:   origin,
		Instance: instance,
		Expires:  now.Add(desktopCallbackLifetime).Unix(),
	}
	temporary, err := os.CreateTemp(directory, ".callback-")
	if err != nil {
		return fmt.Errorf("create desktop callback: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		err = json.NewEncoder(temporary).Encode(record)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write desktop callback: %w", err)
	}
	if err = os.Link(temporaryPath, desktopCallbackPath(directory, key)); err != nil {
		return fmt.Errorf("publish desktop callback: %w", err)
	}
	return nil
}

// ResolveDesktopCallback consumes the application instance registered for an
// OAuth response. A callback registered by another package is left untouched.
func ResolveDesktopCallback(directory, callbackURI, origin string) (string, bool, error) {
	key, ok := responseCallbackKey(callbackURI)
	if !ok || directory == "" || origin == "" {
		return "", false, nil
	}
	if err := validateDesktopCallbackTarget(origin, ""); err != nil {
		return "", false, err
	}
	path := desktopCallbackPath(directory, key)
	record, err := readDesktopCallback(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if record.Expires < desktopCallbackNow().Unix() {
		_ = os.Remove(path)
		return "", false, nil
	}
	if record.Origin != origin {
		return "", false, nil
	}
	if err = validateDesktopCallbackTarget(record.Origin, record.Instance); err != nil {
		return "", false, err
	}
	claim, err := os.CreateTemp(directory, ".callback-claim-*.json")
	if err != nil {
		return "", false, fmt.Errorf("claim desktop callback: %w", err)
	}
	claimPath := claim.Name()
	if closeErr := claim.Close(); closeErr != nil {
		_ = os.Remove(claimPath)
		return "", false, fmt.Errorf("claim desktop callback: %w", closeErr)
	}
	if err = os.Remove(claimPath); err != nil {
		return "", false, fmt.Errorf("claim desktop callback: %w", err)
	}
	if err = os.Rename(path, claimPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("claim desktop callback: %w", err)
	}
	defer os.Remove(claimPath)
	claimed, err := readDesktopCallback(claimPath)
	if err != nil {
		return "", false, err
	}
	if claimed != record || claimed.Origin != origin || claimed.Expires < desktopCallbackNow().Unix() {
		return "", false, errors.New("desktop callback changed while it was claimed")
	}
	return record.Instance, true, nil
}

func authorizationCallbackKey(value string) (string, bool) {
	parsed, query, ok := parseDesktopCallbackURI(value)
	if !ok || !strings.EqualFold(parsed.Scheme, "https") && !strings.EqualFold(parsed.Scheme, "http") {
		return "", false
	}
	states := query["state"]
	redirects := query["redirect_uri"]
	if len(states) != 1 || len(redirects) != 1 || !validDesktopCallbackState(states[0]) {
		return "", false
	}
	redirect, redirectQuery, ok := parseDesktopCallbackURI(redirects[0])
	if !ok || len(redirectQuery) != 0 || !validDesktopCallbackBase(redirect) {
		return "", false
	}
	return desktopCallbackKey(redirect, states[0]), true
}

func responseCallbackKey(value string) (string, bool) {
	parsed, query, ok := parseDesktopCallbackURI(value)
	if !ok || !validDesktopCallbackBase(parsed) {
		return "", false
	}
	states := query["state"]
	codes := query["code"]
	errors := query["error"]
	if len(states) != 1 || !validDesktopCallbackState(states[0]) || (len(codes) == 1) == (len(errors) == 1) || len(codes) > 1 || len(errors) > 1 {
		return "", false
	}
	return desktopCallbackKey(parsed, states[0]), true
}

func parseDesktopCallbackURI(value string) (*url.URL, url.Values, bool) {
	if value == "" || len(value) > maxDesktopCallbackURI || strings.Count(value, "&") > 64 || strings.ContainsAny(value, "\x00\r\n") {
		return nil, nil, false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Fragment != "" || parsed.User != nil {
		return nil, nil, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, nil, false
	}
	return parsed, query, true
}

func validDesktopCallbackBase(parsed *url.URL) bool {
	scheme := strings.ToLower(parsed.Scheme)
	return scheme != "" && scheme != "http" && scheme != "https" && scheme != "file" &&
		(parsed.Host != "" || parsed.Path != "" || parsed.Opaque != "")
}

func validDesktopCallbackState(state string) bool {
	if len(state) < 32 || len(state) > 2048 {
		return false
	}
	for _, character := range state {
		if character < 33 || character > 126 {
			return false
		}
	}
	return true
}

func desktopCallbackKey(parsed *url.URL, state string) string {
	copy := *parsed
	copy.Scheme = strings.ToLower(copy.Scheme)
	copy.Host = strings.ToLower(copy.Host)
	copy.RawQuery = ""
	copy.ForceQuery = false
	copy.Fragment = ""
	digest := sha256.Sum256([]byte(copy.String() + "\x00" + state))
	return hex.EncodeToString(digest[:])
}

func desktopCallbackPath(directory, key string) string {
	return filepath.Join(directory, key+".json")
}

func validateDesktopCallbackTarget(origin, instance string) error {
	if origin == "" {
		return errors.New("desktop callback origin is required")
	}
	if len(origin) > 512 || strings.ContainsAny(origin, "\x00\r\n") {
		return errors.New("desktop callback origin is invalid")
	}
	if len(instance) > 512 || strings.ContainsAny(instance, "\x00\r\n") {
		return errors.New("desktop callback instance is invalid")
	}
	return nil
}

func prepareDesktopCallbackDirectory(directory string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("desktop callback directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create desktop callback directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	details, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || details.Uid != uint32(os.Getuid()) {
		return errors.New("desktop callback directory is not private")
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("desktop callback directory is not private")
	}
	return nil
}

func readDesktopCallback(path string) (desktopCallbackRecord, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return desktopCallbackRecord{}, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		unix.Close(descriptor)
		return desktopCallbackRecord{}, errors.New("open desktop callback")
	}
	defer file.Close()
	var details syscall.Stat_t
	if err = syscall.Fstat(descriptor, &details); err != nil || details.Uid != uint32(os.Getuid()) || details.Mode&0077 != 0 || details.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return desktopCallbackRecord{}, errors.New("invalid desktop callback")
	}
	var record desktopCallbackRecord
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return desktopCallbackRecord{}, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return desktopCallbackRecord{}, errors.New("invalid desktop callback")
	}
	return record, nil
}

func cleanupDesktopCallbacks(directory string, now time.Time) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	active := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		record, readErr := readDesktopCallback(path)
		if readErr != nil || record.Expires < now.Unix() {
			_ = os.Remove(path)
			continue
		}
		active++
	}
	return active
}
