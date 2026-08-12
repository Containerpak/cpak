/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	DefaultRegistryDirectory = "/var/lib/cpak/sessions"
	DefaultSessionDirectory  = "/usr/local/share/wayland-sessions"
	DefaultSystemSessions    = "/usr/share/wayland-sessions"
	DefaultLauncherPath      = "/usr/local/bin/cpak"
)

var sessionIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)
var originPartPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type Session struct {
	ID          string `json:"id"`
	Origin      string `json:"origin"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
}

func (s Session) Validate() error {
	if len(s.ID) == 0 || len(s.ID) > 96 || !sessionIDPattern.MatchString(s.ID) {
		return errors.New("invalid session identifier")
	}
	if err := validateOrigin(s.Origin); err != nil {
		return err
	}
	if err := validateText(s.Name, 80, false); err != nil {
		return errors.New("invalid session name")
	}
	if err := validateText(s.Description, 160, true); err != nil {
		return errors.New("invalid session description")
	}
	if s.Kind != "desktop" && s.Kind != "kiosk" {
		return errors.New("invalid session kind")
	}
	return nil
}

func validateOrigin(origin string) error {
	if len(origin) == 0 || len(origin) > 263 || origin != strings.ToLower(origin) {
		return errors.New("invalid package origin")
	}
	parts := strings.Split(origin, "/")
	if len(parts) != 3 {
		return errors.New("invalid package origin")
	}
	for _, part := range parts {
		if len(part) > 100 || !originPartPattern.MatchString(part) || strings.Contains(part, "..") {
			return errors.New("invalid package origin")
		}
	}
	if !strings.Contains(parts[0], ".") {
		return errors.New("invalid package origin")
	}
	return nil
}

func validateText(value string, limit int, empty bool) error {
	if (!empty && value == "") || len(value) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid text")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return errors.New("invalid text")
		}
	}
	return nil
}

type Registry struct {
	RegistryDirectory string
	SessionDirectory  string
	SystemSessions    string
	LauncherPath      string
	OwnerUID          uint32
}

func DefaultRegistry() Registry {
	return Registry{
		RegistryDirectory: DefaultRegistryDirectory,
		SessionDirectory:  DefaultSessionDirectory,
		SystemSessions:    DefaultSystemSessions,
		LauncherPath:      DefaultLauncherPath,
		OwnerUID:          0,
	}
}

func (r Registry) Register(session Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	if err := r.Prepare(); err != nil {
		return err
	}
	existing, err := r.Load(session.ID)
	if err == nil && existing.Origin != session.Origin {
		return errors.New("session identifier belongs to another package")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := r.ensureSessionSlot(session.ID, existing); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	data = append(data, '\n')
	if err := writeAtomic(filepath.Join(r.RegistryDirectory, session.ID+".json"), data, 0644); err != nil {
		return fmt.Errorf("write session registry: %w", err)
	}
	if err := writeAtomic(filepath.Join(r.SessionDirectory, session.ID+".desktop"), r.desktopEntry(session), 0644); err != nil {
		_ = os.Remove(filepath.Join(r.RegistryDirectory, session.ID+".json"))
		return fmt.Errorf("write display manager session: %w", err)
	}
	return nil
}

func (r Registry) Remove(id, origin string) error {
	if len(id) == 0 || len(id) > 96 || !sessionIDPattern.MatchString(id) {
		return errors.New("invalid session identifier")
	}
	if err := validateOrigin(origin); err != nil {
		return err
	}
	if err := r.validate(); err != nil {
		return err
	}
	existing, err := r.Load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if existing.Origin != origin {
		return errors.New("session identifier belongs to another package")
	}
	for _, path := range []string{
		filepath.Join(r.RegistryDirectory, id+".json"),
		filepath.Join(r.SessionDirectory, id+".desktop"),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session: %w", err)
		}
	}
	return nil
}

func (r Registry) Purge() error {
	if err := r.validate(); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.RegistryDirectory)
	if err != nil {
		return fmt.Errorf("read registered sessions: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		session, err := r.Load(id)
		if err != nil {
			return err
		}
		if err := r.Remove(id, session.Origin); err != nil {
			return err
		}
	}
	return nil
}

func (r Registry) Load(id string) (Session, error) {
	if len(id) == 0 || len(id) > 96 || !sessionIDPattern.MatchString(id) {
		return Session{}, errors.New("invalid session identifier")
	}
	if !filepath.IsAbs(r.RegistryDirectory) {
		return Session{}, errors.New("system authority registry path must be absolute")
	}
	if err := validateExistingDirectory(r.RegistryDirectory, r.OwnerUID); err != nil {
		return Session{}, err
	}
	path := filepath.Join(r.RegistryDirectory, id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return Session{}, fmt.Errorf("read registered session: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return Session{}, errors.New("registered session is not trusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != r.OwnerUID {
		return Session{}, errors.New("registered session has an unexpected owner")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}, fmt.Errorf("read registered session: %w", err)
	}
	if len(data) > 4096 {
		return Session{}, errors.New("registered session is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	session := Session{}
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode registered session: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Session{}, errors.New("registered session contains multiple JSON values")
	}
	if session.ID != id {
		return Session{}, errors.New("registered session identifier does not match its file")
	}
	if err := session.Validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (r Registry) validate() error {
	if !filepath.IsAbs(r.RegistryDirectory) || !filepath.IsAbs(r.SessionDirectory) || !filepath.IsAbs(r.LauncherPath) ||
		(r.SystemSessions != "" && !filepath.IsAbs(r.SystemSessions)) {
		return errors.New("system authority paths must be absolute")
	}
	for _, path := range []string{r.RegistryDirectory, r.SessionDirectory} {
		if err := ensureDirectory(path, r.OwnerUID); err != nil {
			return err
		}
	}
	return nil
}

func (r Registry) Prepare() error {
	if err := r.validate(); err != nil {
		return err
	}
	return r.syncSystemSessions()
}

func (r Registry) syncSystemSessions() error {
	if r.SystemSessions == "" {
		return nil
	}
	if err := validateExistingDirectory(r.SystemSessions, r.OwnerUID); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(r.SystemSessions)
	if err != nil {
		return fmt.Errorf("read system sessions: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Base(name) != name || !strings.HasSuffix(name, ".desktop") {
			continue
		}
		source := filepath.Join(r.SystemSessions, name)
		if err := validateSystemSessionFile(source, r.SystemSessions, r.OwnerUID); err != nil {
			continue
		}
		destination := filepath.Join(r.SessionDirectory, name)
		info, err := os.Lstat(destination)
		if os.IsNotExist(err) {
			if err := os.Symlink(source, destination); err != nil {
				return fmt.Errorf("link system session: %w", err)
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(destination)
		if err != nil || target != source {
			continue
		}
	}
	return nil
}

func validateSystemSessionFile(path, root string, owner uint32) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, filepath.Clean(root)+string(filepath.Separator)) {
		return errors.New("system session points outside its directory")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("system session is not trusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner {
		return errors.New("system session has an unexpected owner")
	}
	return nil
}

func (r Registry) ensureSessionSlot(id string, existing Session) error {
	path := filepath.Join(r.SessionDirectory, id+".desktop")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect display manager session: %w", err)
	}
	if existing.ID == id && info.Mode().IsRegular() && info.Mode().Perm()&0022 == 0 {
		return nil
	}
	return errors.New("session identifier is already used by the system")
}

func ensureDirectory(path string, owner uint32) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("create system authority directory: %w", err)
	}
	return validateExistingDirectory(path, owner)
}

func validateExistingDirectory(path string, owner uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read system authority directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("system authority directory is invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || info.Mode().Perm()&0022 != 0 {
		return errors.New("system authority directory is not trusted")
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".cpak-session-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (r Registry) desktopEntry(session Session) []byte {
	content := "[Desktop Entry]\n" +
		"Name=" + session.Name + "\n" +
		"Comment=" + session.Description + "\n" +
		"Exec=" + r.LauncherPath + " session launch " + session.ID + "\n" +
		"TryExec=" + r.LauncherPath + "\n" +
		"Type=Application\n" +
		"DesktopNames=" + session.ID + "\n" +
		"X-cpak-Origin=" + session.Origin + "\n" +
		"X-cpak-SessionKind=" + session.Kind + "\n"
	return []byte(content)
}
