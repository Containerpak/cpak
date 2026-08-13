/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package registryauth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

// Record binds one package origin to the exact OCI repository it may access.
type Record struct {
	Origin     string   `json:"origin"`
	Registry   string   `json:"registry"`
	Repository string   `json:"repository"`
	Username   string   `json:"username,omitempty"`
	TokenHosts []string `json:"token_hosts,omitempty"`
	SecretFile string   `json:"secret_file,omitempty"`
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
	stored := record.SecretFile == ""
	if stored {
		if err := storeSecret(ctx, record, secret); err != nil {
			return err
		}
	}
	previous := make([]Record, 0, 1)
	filtered := records[:0]
	for _, current := range records {
		if current.Origin == record.Origin && current.Registry == record.Registry && current.Repository == record.Repository {
			previous = append(previous, current)
			continue
		}
		filtered = append(filtered, current)
	}
	records = append(filtered, record)
	if err = writeRecords(path, records); err != nil {
		if stored {
			_ = clearSecret(ctx, record)
		}
		return err
	}
	for _, current := range previous {
		if current.SecretFile != "" || secretBinding(current) == secretBinding(record) {
			continue
		}
		if err = clearSecret(ctx, current); err != nil {
			return err
		}
	}
	return nil
}

// ReadSecretFile reads a user-owned secret file without accepting broad permissions.
func ReadSecretFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm()&0077 != 0 || !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() {
		return "", fmt.Errorf("registryauth: secret file must be a user-owned regular file with mode 0600")
	}
	content, err := os.ReadFile(path)
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
	for _, record := range records {
		if record.Origin == origin {
			if record.SecretFile == "" {
				if err = clearSecret(ctx, record); err != nil {
					return err
				}
			}
			continue
		}
		filtered = append(filtered, record)
	}
	return writeRecords(path, filtered)
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
		return records[i].Origin+records[i].Registry+records[i].Repository < records[j].Origin+records[j].Registry+records[j].Repository
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

func credentialFromFile(path, origin string, ref oci.Reference) (oci.Credential, error) {
	info, err := os.Stat(path)
	if err != nil {
		return oci.Credential{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm()&0077 != 0 || !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() {
		return oci.Credential{}, fmt.Errorf("registryauth: injected credential file must be a regular file with mode 0600")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return oci.Credential{}, err
	}
	var file credentialFile
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&file); err != nil {
		return oci.Credential{}, fmt.Errorf("registryauth: decode injected credentials: %w", err)
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

func recordSecret(ctx context.Context, record Record) (string, error) {
	if record.SecretFile != "" {
		return ReadSecretFile(record.SecretFile)
	}
	return lookupSecret(ctx, record)
}

func secretAttributes(record Record) map[string]string {
	return map[string]string{
		"application": "cpak",
		"origin":      record.Origin,
		"registry":    record.Registry,
		"repository":  record.Repository,
		"username":    record.Username,
		"token-hosts": secretTokenHosts(record.TokenHosts),
	}
}

func secretBinding(record Record) string {
	return record.Origin + "\x00" + record.Registry + "\x00" + record.Repository + "\x00" + record.Username + "\x00" + secretTokenHosts(record.TokenHosts) + "\x00" + record.SecretFile
}

func secretTokenHosts(hosts []string) string {
	values := append([]string{}, hosts...)
	for i := range values {
		values[i] = strings.ToLower(values[i])
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}
