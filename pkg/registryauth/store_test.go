/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package registryauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

func TestInjectedCredentialRequiresExactBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"records":[{"origin":"github.com/example/app","registry":"ghcr.io","repository":"example/app","username":"user","password":"secret"}]}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ref, err := oci.ParseReference("ghcr.io/example/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentialFromFile(path, "github.com/example/app", ref)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != "user" || credential.Password != "secret" {
		t.Fatalf("unexpected credential: %+v", credential)
	}
	credential, err = credentialFromFile(path, "github.com/example/other", ref)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != "" || credential.Password != "" || credential.AccessToken != "" || len(credential.TokenHosts) != 0 {
		t.Fatalf("credential escaped its origin binding: %+v", credential)
	}
}

func TestInjectedCredentialRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"records":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	ref, _ := oci.ParseReference("ghcr.io/example/app")
	if _, err := credentialFromFile(path, "github.com/example/app", ref); err == nil {
		t.Fatal("credential file with broad permissions was accepted")
	}
}

func TestProviderUsesInjectedCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"records":[{"origin":"github.com/example/app","registry":"ghcr.io","repository":"example/app","access_token":"token"}]}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_REGISTRY_AUTH_FILE", path)
	ref, _ := oci.ParseReference("ghcr.io/example/app")
	credential, err := (Provider{Origin: "github.com/example/app"}).Credential(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "token" {
		t.Fatalf("unexpected credential: %+v", credential)
	}
}

func TestLoadKeepsBindingsWrittenBeforeHeadlessSupport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry-auth.json")
	content := `[{"origin":"github.com/example/app","registry":"ghcr.io","repository":"example/app","username":"account"}]`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Origin != "github.com/example/app" || records[0].SecretFile != "" {
		t.Fatalf("unexpected legacy binding: %+v", records)
	}
}

func TestSecretAttributesIncludeEveryBinding(t *testing.T) {
	record := Record{Origin: "github.com/example/app", Registry: "ghcr.io", Repository: "example/app", Username: "account", TokenHosts: []string{"Auth.Example.com", "token.example.com"}}
	attributes := secretAttributes(record)
	if attributes["application"] != "cpak" || attributes["origin"] != record.Origin || attributes["registry"] != record.Registry || attributes["repository"] != record.Repository || attributes["username"] != record.Username || attributes["token-hosts"] != "auth.example.com,token.example.com" {
		t.Fatalf("unexpected attributes: %+v", attributes)
	}
}

func TestSecretBindingChangesWithCredentialAuthority(t *testing.T) {
	base := Record{Origin: "github.com/example/app", Registry: "ghcr.io", Repository: "example/app", Username: "account", TokenHosts: []string{"auth.example.com"}}
	reordered := base
	reordered.TokenHosts = []string{"AUTH.EXAMPLE.COM"}
	if secretBinding(base) != secretBinding(reordered) {
		t.Fatal("equivalent token hosts changed the binding")
	}
	changed := base
	changed.TokenHosts = []string{"attacker.example.com"}
	if secretBinding(base) == secretBinding(changed) {
		t.Fatal("token host did not change the binding")
	}
	changed = base
	changed.Username = "other"
	if secretBinding(base) == secretBinding(changed) {
		t.Fatal("username did not change the binding")
	}
}

func TestHeadlessBindingKeepsTheSecretInItsSourceFile(t *testing.T) {
	directory := t.TempDir()
	secretPath := filepath.Join(directory, "token")
	bindingsPath := filepath.Join(directory, "registry-auth.json")
	if err := os.WriteFile(secretPath, []byte("headless-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	record := Record{
		Origin:     "github.com/example/app",
		Registry:   "ghcr.io",
		Repository: "example/app",
		SecretFile: secretPath,
	}
	if err := Save(context.Background(), bindingsPath, record, "headless-token"); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(bindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "headless-token") {
		t.Fatal("secret was copied into public binding metadata")
	}
	ref, _ := oci.ParseReference("ghcr.io/example/app")
	credential, err := (Provider{Origin: record.Origin, Path: bindingsPath}).Credential(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken != "headless-token" {
		t.Fatalf("unexpected credential: %+v", credential)
	}
	if err = Remove(context.Background(), bindingsPath, record.Origin); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(secretPath); err != nil {
		t.Fatalf("logout removed the user secret file: %v", err)
	}
}
