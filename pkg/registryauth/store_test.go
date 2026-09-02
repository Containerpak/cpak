/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package registryauth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
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

func TestInjectedSourceCredentialRequiresExactBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"records":[{"origin":"github.com/example/app","source_host":"github.com","registry":"ghcr.io","repository":"example/app","username":"user","password":"secret"}]}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	credential, err := sourceCredentialFromFile(path, "github.com/example/app", "github.com")
	if err != nil || credential != "secret" {
		t.Fatalf("unexpected source credential: %q %v", credential, err)
	}
	for _, scope := range [][2]string{{"github.com/example/other", "github.com"}, {"github.com/example/app", "attacker.example.com"}} {
		credential, err = sourceCredentialFromFile(path, scope[0], scope[1])
		if err != nil || credential != "" {
			t.Fatalf("source credential escaped binding %v: %q %v", scope, credential, err)
		}
	}
}

func TestSourceCredentialUsesInjectedCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"records":[{"origin":"github.com/example/app","source_host":"github.com","registry":"","repository":"","access_token":"source-token"}]}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_REGISTRY_AUTH_FILE", path)
	credential, err := SourceCredential(context.Background(), "unused", "github.com/example/app", "GITHUB.COM")
	if err != nil || credential != "source-token" {
		t.Fatalf("unexpected source credential: %q %v", credential, err)
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
	record := Record{Origin: "github.com/example/app", SourceHost: "github.com", Registry: "ghcr.io", Repository: "example/app", Username: "account", TokenHosts: []string{"Auth.Example.com", "token.example.com"}}
	attributes := secretAttributes(record)
	if attributes["application"] != "cpak" || attributes["origin"] != record.Origin || attributes["source-host"] != record.SourceHost || attributes["registry"] != record.Registry || attributes["repository"] != record.Repository || attributes["username"] != record.Username || attributes["token-hosts"] != "auth.example.com,token.example.com" {
		t.Fatalf("unexpected attributes: %+v", attributes)
	}
}

func TestLegacySecretAttributesRemainCompatible(t *testing.T) {
	attributes := secretAttributes(Record{Origin: "github.com/example/app", Registry: "ghcr.io", Repository: "example/app", Username: "account"})
	if _, exists := attributes["source-host"]; exists {
		t.Fatalf("legacy secret binding gained a source host: %+v", attributes)
	}
}

func TestSecretBindingChangesWithCredentialAuthority(t *testing.T) {
	base := Record{Origin: "github.com/example/app", SourceHost: "github.com", Registry: "ghcr.io", Repository: "example/app", Username: "account", TokenHosts: []string{"auth.example.com"}}
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
	changed.SourceHost = "attacker.example.com"
	if secretBinding(base) == secretBinding(changed) {
		t.Fatal("source host did not change the binding")
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
		SourceHost: "github.com",
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
	sourceCredential, err := SourceCredential(context.Background(), bindingsPath, record.Origin, record.SourceHost)
	if err != nil || sourceCredential != "headless-token" {
		t.Fatalf("unexpected source credential: %q %v", sourceCredential, err)
	}
	if err = Remove(context.Background(), bindingsPath, record.Origin); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(secretPath); err != nil {
		t.Fatalf("logout removed the user secret file: %v", err)
	}
}

func TestSaveFallsBackWhenSecretServiceIsUnavailable(t *testing.T) {
	tests := map[string]func(*testing.T) string{
		"no session bus": func(t *testing.T) string {
			return "unix:path=" + filepath.Join(t.TempDir(), "missing-bus")
		},
		"no secret service": emptySessionBus,
	}
	for name, sessionBus := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", sessionBus(t))
			directory := t.TempDir()
			bindingsPath := filepath.Join(directory, "registry-auth.json")
			record := Record{
				Origin:     "github.com/example/private-app",
				SourceHost: "github.com",
				Registry:   "ghcr.io",
				Repository: "example/private-app",
				Username:   "account",
			}
			if err := Save(context.Background(), bindingsPath, record, "github-token"); err != nil {
				t.Fatalf("Secret Service failure did not degrade to a private file: %v", err)
			}
			records, err := Load(bindingsPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].SecretFile == "" {
				t.Fatalf("fallback binding did not reference a secret file: %+v", records)
			}
			secretPath := records[0].SecretFile
			if filepath.Dir(secretPath) != filepath.Join(directory, "credentials") {
				t.Fatalf("fallback escaped the cpak credential directory: %s", secretPath)
			}
			info, err := os.Lstat(secretPath)
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("fallback secret mode is %s", info.Mode())
			}
			metadata, err := os.ReadFile(bindingsPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(metadata), "github-token") {
				t.Fatal("fallback copied the secret into binding metadata")
			}
			ref, _ := oci.ParseReference("ghcr.io/example/private-app:beta")
			credential, err := (Provider{Origin: record.Origin, Path: bindingsPath}).Credential(context.Background(), ref)
			if err != nil || credential.Username != "account" || credential.Password != "github-token" {
				t.Fatalf("fallback registry credential: %+v %v", credential, err)
			}
			sourceCredential, err := SourceCredential(context.Background(), bindingsPath, record.Origin, record.SourceHost)
			if err != nil || sourceCredential != "github-token" {
				t.Fatalf("fallback source credential: %q %v", sourceCredential, err)
			}
			if err = Remove(context.Background(), bindingsPath, record.Origin); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(secretPath); !os.IsNotExist(err) {
				t.Fatalf("logout kept the cpak-managed secret: %v", err)
			}
			records, err = Load(bindingsPath)
			if err != nil || len(records) != 0 {
				t.Fatalf("logout kept the fallback binding: %+v %v", records, err)
			}
		})
	}
}

func TestSaveReplacesCpakManagedSecret(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
	directory := t.TempDir()
	bindingsPath := filepath.Join(directory, "registry-auth.json")
	record := Record{Origin: "github.com/example/app", SourceHost: "github.com"}
	if err := Save(context.Background(), bindingsPath, record, "first-token"); err != nil {
		t.Fatal(err)
	}
	records, err := Load(bindingsPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("first fallback binding: %+v %v", records, err)
	}
	firstPath := records[0].SecretFile
	if err = Save(context.Background(), bindingsPath, record, "second-token"); err != nil {
		t.Fatal(err)
	}
	records, err = Load(bindingsPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("replacement fallback binding: %+v %v", records, err)
	}
	if records[0].SecretFile == firstPath {
		t.Fatal("replacement reused the previous secret file")
	}
	if _, err = os.Lstat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("replacement kept the previous secret: %v", err)
	}
	secret, err := ReadSecretFile(records[0].SecretFile)
	if err != nil || secret != "second-token" {
		t.Fatalf("replacement secret: %q %v", secret, err)
	}
}

func TestSaveReplacesManagedSecretThroughDirectoryAlias(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
	directory := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}
	record := Record{Origin: "github.com/example/app", SourceHost: "github.com"}
	bindingsPath := filepath.Join(directory, "registry-auth.json")
	if err := Save(context.Background(), bindingsPath, record, "first-token"); err != nil {
		t.Fatal(err)
	}
	records, err := Load(bindingsPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("first fallback binding: %+v %v", records, err)
	}
	firstPath := records[0].SecretFile
	if err = Save(context.Background(), filepath.Join(alias, "registry-auth.json"), record, "second-token"); err != nil {
		t.Fatalf("replace through directory alias: %v", err)
	}
	records, err = Load(bindingsPath)
	if err != nil || len(records) != 1 {
		t.Fatalf("replacement fallback binding: %+v %v", records, err)
	}
	if _, err = os.Lstat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("replacement kept the previous secret: %v", err)
	}
	secret, err := ReadSecretFile(records[0].SecretFile)
	if err != nil || secret != "second-token" {
		t.Fatalf("replacement secret: %q %v", secret, err)
	}
}

func TestManagedCredentialDirectoryRequiresPrivatePermissions(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "credentials"), 0755); err != nil {
		t.Fatal(err)
	}
	err := Save(
		context.Background(),
		filepath.Join(directory, "registry-auth.json"),
		Record{Origin: "github.com/example/app", SourceHost: "github.com"},
		"token",
	)
	if err == nil || !strings.Contains(err.Error(), "mode 0700") {
		t.Fatalf("broad credential directory was accepted: %v", err)
	}
}

func TestRemoveManagedSecretRejectsAnotherDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(t.TempDir(), ".credential-foreign")
	if err := os.WriteFile(secretPath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	err := removeManagedSecret(
		filepath.Join(directory, "registry-auth.json"),
		Record{SecretFile: secretPath, SecretFileManaged: true},
	)
	if err == nil || !strings.Contains(err.Error(), "outside the credential directory") {
		t.Fatalf("foreign managed secret was accepted: %v", err)
	}
	if _, err = os.Stat(secretPath); err != nil {
		t.Fatalf("foreign secret was removed: %v", err)
	}
}

func TestReadSecretFileRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretFile(link); err == nil {
		t.Fatal("symlink secret file was accepted")
	}
}

func TestLogoutDoesNotRequireSecretService(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing-bus"))
	bindingsPath := filepath.Join(t.TempDir(), "registry-auth.json")
	content := `[{"origin":"github.com/example/app","source_host":"github.com"}]`
	if err := os.WriteFile(bindingsPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Remove(context.Background(), bindingsPath, "github.com/example/app"); err != nil {
		t.Fatalf("logout depended on Secret Service: %v", err)
	}
	records, err := Load(bindingsPath)
	if err != nil || len(records) != 0 {
		t.Fatalf("logout kept the binding: %+v %v", records, err)
	}
}

func TestOnlyMissingSecretServiceErrorsUseTheFallback(t *testing.T) {
	for _, name := range []string{
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.Spawn.ExecFailed",
		"org.freedesktop.DBus.Error.Spawn.ChildExited",
		"org.freedesktop.DBus.Error.Spawn.Failed",
	} {
		if !secretServiceActivationUnavailable(dbus.Error{Name: name}) {
			t.Errorf("%s was not treated as unavailable", name)
		}
	}
	if secretServiceActivationUnavailable(dbus.Error{Name: "org.freedesktop.DBus.Error.AccessDenied"}) {
		t.Fatal("access denial was treated as an unavailable Secret Service")
	}
}

func emptySessionBus(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is unavailable")
	}
	output, err := exec.Command(binary, "--session", "--fork", "--print-address=1", "--print-pid=1").Output()
	if err != nil {
		t.Fatalf("start private session bus: %v", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		t.Fatalf("private session bus returned %q", output)
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse private session bus pid: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	})
	return fields[0]
}
