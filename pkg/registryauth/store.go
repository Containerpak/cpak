/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"golang.org/x/sys/unix"
)

// Record binds one package origin to the exact source and OCI repository it may access.
type Record struct {
	Origin            string   `json:"origin"`
	SourceHost        string   `json:"source_host,omitempty"`
	Registry          string   `json:"registry"`
	Repository        string   `json:"repository"`
	Username          string   `json:"username,omitempty"`
	TokenHosts        []string `json:"token_hosts,omitempty"`
	SecretFile        string   `json:"secret_file,omitempty"`
	SecretFileManaged bool     `json:"secret_file_managed,omitempty"`
}

type credentialFile struct {
	Records []injectedRecord `json:"records"`
}

type injectedRecord struct {
	Record
	Password    string `json:"password,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}

// Provider supplies credentials only when origin, registry and repository match.
type Provider struct {
	Origin string
	Path   string
}

// SourceCredential returns a token only for an exact package origin and source host.
func SourceCredential(ctx context.Context, path, origin, host string) (string, error) {
	host = strings.ToLower(host)
	if injectedPath := os.Getenv("CPAK_REGISTRY_AUTH_FILE"); injectedPath != "" {
		return sourceCredentialFromFile(injectedPath, origin, host)
	}
	records, err := Load(path)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Origin != origin || record.SourceHost != host {
			continue
		}
		return recordSecret(ctx, record)
	}
	return "", nil
}

// Credential implements oci.CredentialProvider.
func (p Provider) Credential(ctx context.Context, ref oci.Reference) (oci.Credential, error) {
	if path := os.Getenv("CPAK_REGISTRY_AUTH_FILE"); path != "" {
		return credentialFromFile(path, p.Origin, ref)
	}
	records, err := Load(p.Path)
	if err != nil {
		return oci.Credential{}, err
	}
	for _, record := range records {
		if record.Origin != p.Origin || record.Registry != ref.Registry || record.Repository != ref.Repository {
			continue
		}
		secret, err := recordSecret(ctx, record)
		if err != nil {
			return oci.Credential{}, err
		}
		credential := oci.Credential{Username: record.Username, TokenHosts: append([]string{}, record.TokenHosts...)}
		if record.Username == "" {
			credential.AccessToken = secret
		} else {
			credential.Password = secret
		}
		return credential, nil
	}
	return oci.Credential{}, nil
}

// Save stores public binding metadata and a desktop secret when needed.
func Save(ctx context.Context, path string, record Record, secret string) error {
	if secret == "" {
		return fmt.Errorf("registryauth: secret is required")
	}
	record.SourceHost = strings.ToLower(record.SourceHost)
	if err := validateRecord(record); err != nil {
		return err
	}
	if record.SecretFile != "" {
		if !filepath.IsAbs(record.SecretFile) {
			return fmt.Errorf("registryauth: secret file path must be absolute")
		}
		current, err := ReadSecretFile(record.SecretFile)
		if err != nil {
			return err
		}
		if current != secret {
			return fmt.Errorf("registryauth: secret file changed while it was read")
		}
	}
	records, err := Load(path)
	if err != nil {
		return err
	}
	desktopStored := false
	managedStored := false
	if record.SecretFile == "" {
		if err = storeSecret(ctx, record, secret); errors.Is(err, errSecretServiceUnavailable) {
			record.SecretFile, err = writeManagedSecret(path, secret)
			if err != nil {
				return err
			}
			record.SecretFileManaged = true
			managedStored = true
		} else if err != nil {
			return err
		} else {
			desktopStored = true
		}
	}
	previous := make([]Record, 0, 1)
	filtered := records[:0]
	for _, current := range records {
		if sameRegistryScope(current, record) || sameSourceScope(current, record) {
			previous = append(previous, current)
			continue
		}
		filtered = append(filtered, current)
	}
	records = append(filtered, record)
	if err = writeRecords(path, records); err != nil {
		if desktopStored {
			_ = clearSecret(ctx, record)
		}
		if managedStored {
			_ = removeManagedSecret(path, record)
		}
		return err
	}
	for _, current := range previous {
		if current.SecretFileManaged {
			if err = removeManagedSecret(path, current); err != nil {
				return err
			}
			continue
		}
		if current.SecretFile != "" || secretBinding(current) == secretBinding(record) {
			continue
		}
		if err = clearSecret(ctx, current); err != nil && !errors.Is(err, errSecretServiceUnavailable) {
			return err
		}
	}
	return nil
}

// ReadSecretFile reads a user-owned secret file without accepting broad permissions.
func ReadSecretFile(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm()&0077 != 0 || !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() {
		return "", fmt.Errorf("registryauth: secret file must be a user-owned regular file with mode 0600")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSuffix(string(content), "\n")
	if secret == "" {
		return "", fmt.Errorf("registryauth: secret file is empty")
	}
	return secret, nil
}

// Remove deletes every credential binding for an origin.
func Remove(ctx context.Context, path, origin string) error {
	records, err := Load(path)
	if err != nil {
		return err
	}
	filtered := make([]Record, 0, len(records))
	removed := make([]Record, 0, len(records))
	for _, record := range records {
		if record.Origin == origin {
			removed = append(removed, record)
			continue
		}
		filtered = append(filtered, record)
	}
	if err = writeRecords(path, filtered); err != nil {
		return err
	}
	for _, record := range removed {
		if record.SecretFileManaged {
			if err = removeManagedSecret(path, record); err != nil {
				return err
			}
			continue
		}
		if record.SecretFile != "" {
			continue
		}
		if err = clearSecret(ctx, record); err != nil && !errors.Is(err, errSecretServiceUnavailable) {
			return err
		}
	}
	return nil
}

// Load reads public credential bindings.
func Load(path string) ([]Record, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	if err = json.Unmarshal(content, &records); err != nil {
		return nil, fmt.Errorf("registryauth: decode bindings: %w", err)
	}
	return records, nil
}

func writeRecords(path string, records []Record) error {
	sort.Slice(records, func(i, j int) bool {
		return records[i].Origin+records[i].SourceHost+records[i].Registry+records[i].Repository < records[j].Origin+records[j].SourceHost+records[j].Registry+records[j].Repository
	})
	content, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".registry-auth-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(content)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeManagedSecret(bindingsPath, secret string) (string, error) {
	directory, err := managedSecretDirectory(bindingsPath)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ok || int(stat.Uid) != os.Getuid() {
		return "", fmt.Errorf("registryauth: credential directory must be a user-owned directory with mode 0700")
	}
	file, err := os.CreateTemp(directory, ".credential-*")
	if err != nil {
		return "", err
	}
	secretPath := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(secretPath)
		}
	}()
	if err = file.Chmod(0600); err == nil {
		_, err = file.WriteString(secret)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	keep = true
	return secretPath, nil
}

func removeManagedSecret(bindingsPath string, record Record) error {
	directory, err := managedSecretDirectory(bindingsPath)
	if err != nil {
		return err
	}
	secretPath, err := filepath.Abs(record.SecretFile)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(secretPath), ".credential-") {
		return fmt.Errorf("registryauth: managed secret path is outside the credential directory")
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		return err
	}
	secretDirectoryInfo, err := os.Stat(filepath.Dir(secretPath))
	if err != nil {
		return err
	}
	if !os.SameFile(directoryInfo, secretDirectoryInfo) {
		return fmt.Errorf("registryauth: managed secret path is outside the credential directory")
	}
	if err = os.Remove(secretPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func managedSecretDirectory(bindingsPath string) (string, error) {
	return filepath.Abs(filepath.Join(filepath.Dir(bindingsPath), "credentials"))
}

func credentialFromFile(path, origin string, ref oci.Reference) (oci.Credential, error) {
	file, err := readCredentialFile(path)
	if err != nil {
		return oci.Credential{}, err
	}
	for _, record := range file.Records {
		if record.Origin == origin && record.Registry == ref.Registry && record.Repository == ref.Repository {
			if record.AccessToken != "" && (record.Username != "" || record.Password != "") {
				return oci.Credential{}, fmt.Errorf("registryauth: injected credential mixes token and basic authentication")
			}
			return oci.Credential{Username: record.Username, Password: record.Password, AccessToken: record.AccessToken, TokenHosts: append([]string{}, record.TokenHosts...)}, nil
		}
	}
	return oci.Credential{}, nil
}

func sourceCredentialFromFile(path, origin, host string) (string, error) {
	file, err := readCredentialFile(path)
	if err != nil {
		return "", err
	}
	for _, record := range file.Records {
		if record.Origin != origin || strings.ToLower(record.SourceHost) != host {
			continue
		}
		if record.AccessToken != "" && record.Password != "" {
			return "", fmt.Errorf("registryauth: injected source credential contains two secrets")
		}
		if record.AccessToken != "" {
			return record.AccessToken, nil
		}
		if record.Password != "" {
			return record.Password, nil
		}
		return "", fmt.Errorf("registryauth: injected source credential is empty")
	}
	return "", nil
}

func readCredentialFile(path string) (credentialFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return credentialFile{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm()&0077 != 0 || !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() {
		return credentialFile{}, fmt.Errorf("registryauth: injected credential file must be a regular file with mode 0600")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return credentialFile{}, err
	}
	var file credentialFile
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&file); err != nil {
		return credentialFile{}, fmt.Errorf("registryauth: decode injected credentials: %w", err)
	}
	return file, nil
}

func recordSecret(ctx context.Context, record Record) (string, error) {
	if record.SecretFile != "" {
		return ReadSecretFile(record.SecretFile)
	}
	return lookupSecret(ctx, record)
}

func secretAttributes(record Record) map[string]string {
	attributes := map[string]string{
		"application": "cpak",
		"origin":      record.Origin,
		"registry":    record.Registry,
		"repository":  record.Repository,
		"username":    record.Username,
		"token-hosts": secretTokenHosts(record.TokenHosts),
	}
	if record.SourceHost != "" {
		attributes["source-host"] = record.SourceHost
	}
	return attributes
}

func secretBinding(record Record) string {
	return record.Origin + "\x00" + record.SourceHost + "\x00" + record.Registry + "\x00" + record.Repository + "\x00" + record.Username + "\x00" + secretTokenHosts(record.TokenHosts) + "\x00" + record.SecretFile
}

func validateRecord(record Record) error {
	if record.Origin == "" {
		return fmt.Errorf("registryauth: package origin is required")
	}
	if record.SourceHost != "" && (strings.ContainsAny(record.SourceHost, "/@") || record.SourceHost != strings.TrimSpace(record.SourceHost)) {
		return fmt.Errorf("registryauth: invalid source host")
	}
	if (record.Registry == "") != (record.Repository == "") {
		return fmt.Errorf("registryauth: registry and repository must be set together")
	}
	if record.SourceHost == "" && record.Registry == "" {
		return fmt.Errorf("registryauth: source or registry scope is required")
	}
	if record.SecretFileManaged && record.SecretFile == "" {
		return fmt.Errorf("registryauth: managed secret path is required")
	}
	return nil
}

func sameRegistryScope(first, second Record) bool {
	return first.Origin == second.Origin && first.Registry != "" && second.Registry != "" && first.Registry == second.Registry && first.Repository == second.Repository
}

func sameSourceScope(first, second Record) bool {
	return first.Origin == second.Origin && first.SourceHost != "" && second.SourceHost != "" && first.SourceHost == second.SourceHost
}

func secretTokenHosts(hosts []string) string {
	values := append([]string{}, hosts...)
	for i := range values {
		values[i] = strings.ToLower(values[i])
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}
