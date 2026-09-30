/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestSecretsUseThePolicyOrigin(t *testing.T) {
	o := testOptions(t)
	o.SecretOrigin = "github.com/example/editor"
	o.SecretCapabilities = map[string]bool{"read-owned": true, "write-owned": true}
	calls := 0
	o.Secrets = func(_ context.Context, origin string, r SecretRequest) (SecretResult, error) {
		calls++
		if origin != o.SecretOrigin {
			t.Fatal("caller selected the keyring origin")
		}
		return SecretResult{Entries: []SecretEntry{{Attributes: r.Attributes, Value: "synthetic"}}}, nil
	}
	startBroker(t, o)
	r := SecretRequest{Operation: "get", Attributes: map[string]string{"account": "one"}}
	result, err := testClient(o).Secrets(context.Background(), r)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].Value != "synthetic" {
		t.Fatalf("scoped lookup: %v", err)
	}
	for _, data := range []string{
		`{"operation":"get","attributes":{"account":"one"},"origin":"github.com/example/other"}`,
		`{"operation":"get","attributes":{"cpak.origin":"other"}}`,
		`{"operation":"get","attributes":{"xdg:schema":"com.containerpak.RegistryAuth"}}`,
		`{"operation":"delete","attributes":{}}`,
	} {
		if err := testClient(o).call(context.Background(), ActionSecrets, json.RawMessage(data)); err == nil {
			t.Fatal("scope override was accepted")
		}
	}
	if calls != 1 {
		t.Fatal("invalid request reached the keyring")
	}
}

func TestSecretsCannotEscalateReadPermission(t *testing.T) {
	o := testOptions(t)
	o.SecretOrigin = "github.com/example/viewer"
	o.SecretCapabilities = map[string]bool{"read-owned": true}
	o.Secrets = func(context.Context, string, SecretRequest) (SecretResult, error) {
		t.Fatal("write reached a read-only backend")
		return SecretResult{}, nil
	}
	startBroker(t, o)
	for _, r := range []SecretRequest{
		{Operation: "put", Attributes: map[string]string{"account": "one"}, Value: "synthetic"},
		{Operation: "delete", Attributes: map[string]string{"account": "one"}},
	} {
		if _, err := testClient(o).Secrets(context.Background(), r); err == nil {
			t.Fatal("write without permission was accepted")
		}
	}
}

func TestSecretsWithoutAGrantAreDenied(t *testing.T) {
	o := testOptions(t)
	startBroker(t, o)
	if _, err := testClient(o).Secrets(context.Background(), SecretRequest{Operation: "search", Attributes: map[string]string{"context": "one"}}); err == nil {
		t.Fatal("default policy allowed keyring access")
	}
}

func TestSecretAttributesSeparateApplicationsAndRegistryCredentials(t *testing.T) {
	attrs := map[string]string{"context": "one", "account": ""}
	a := scopedSecretAttributes("github.com/example/one", attrs)
	b := scopedSecretAttributes("github.com/example/two", attrs)
	if secretAttributesMatch(a, b) || a["xdg:schema"] != "com.containerpak.AppSecrets" {
		t.Fatal("keyring namespaces were shared")
	}
	delete(a, "account")
	if secretAttributesMatch(a, scopedSecretAttributes("github.com/example/one", attrs)) {
		t.Fatal("missing attribute matched an empty value")
	}
}

func TestSecretsRejectOversizedValuesAndBackendLeaks(t *testing.T) {
	if err := validateSecretRequest(SecretRequest{Operation: "put", Attributes: map[string]string{"account": "one"}, Value: strings.Repeat("x", maxSecretSize+1)}); err == nil {
		t.Fatal("oversized value was accepted")
	}
	o := testOptions(t)
	o.SecretOrigin = "github.com/example/editor"
	o.SecretCapabilities = map[string]bool{"read-owned": true}
	o.Secrets = func(context.Context, string, SecretRequest) (SecretResult, error) {
		return SecretResult{}, errors.New("synthetic-secret-must-not-leak")
	}
	startBroker(t, o)
	_, err := testClient(o).Secrets(context.Background(), SecretRequest{Operation: "get", Attributes: map[string]string{"account": "one"}})
	if err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatal("keyring error leaked into the response")
	}
}

func TestSecretShimAcceptsValuesOnlyOnStdin(t *testing.T) {
	o := testOptions(t)
	o.SecretOrigin = "github.com/example/editor"
	o.SecretCapabilities = map[string]bool{"write-owned": true}
	r := SecretRequest{Operation: "put", Attributes: map[string]string{"account": "one"}, Value: "synthetic"}
	o.Secrets = func(_ context.Context, _ string, got SecretRequest) (SecretResult, error) {
		if !reflect.DeepEqual(got, r) {
			t.Fatal("stdin request changed")
		}
		return SecretResult{Changed: true}, nil
	}
	startBroker(t, o)
	data, _ := json.Marshal(r)
	var output bytes.Buffer
	if err := InvokeShim(context.Background(), o.SocketPath, o.Token, "cpak-secrets", nil, nil, bytes.NewReader(data), &output, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if err := InvokeShim(context.Background(), o.SocketPath, o.Token, "cpak-secrets", []string{"synthetic"}, nil, bytes.NewReader(data), io.Discard, io.Discard, false); err == nil {
		t.Fatal("secret in arguments was accepted")
	}
}

func TestSecretCatalogRevokesAccess(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("s", 64)
	p := Policy{SecretOrigin: "github.com/example/editor", SecretCapabilities: map[string]bool{"read-owned": true}}
	if err := WritePolicy(dir, token, p); err != nil {
		t.Fatal(err)
	}
	o, err := resolveCatalogPolicy("/unused.sock", dir, nil, Request{Token: token})
	if err != nil || o.SecretOrigin != p.SecretOrigin || !o.SecretCapabilities["read-owned"] {
		t.Fatal("catalog dropped the scoped grant")
	}
	if err := WritePolicy(dir, token, Policy{AllowOpenURI: true}); err != nil {
		t.Fatal(err)
	}
	o, err = resolveCatalogPolicy("/unused.sock", dir, nil, Request{Token: token})
	if err != nil || len(o.SecretCapabilities) != 0 {
		t.Fatal("catalog kept a revoked grant")
	}
}

func TestSecretRequestsStillRequireTheBrokerToken(t *testing.T) {
	o := testOptions(t)
	o.SecretOrigin = "github.com/example/editor"
	o.SecretCapabilities = map[string]bool{"read-owned": true}
	o.Secrets = func(context.Context, string, SecretRequest) (SecretResult, error) {
		t.Fatal("unauthenticated request reached the keyring")
		return SecretResult{}, nil
	}
	startBroker(t, o)
	c := testClient(o)
	c.Token = strings.Repeat("a", 32)
	if _, err := c.Secrets(context.Background(), SecretRequest{Operation: "get", Attributes: map[string]string{"account": "one"}}); err == nil {
		t.Fatal("wrong broker token was accepted")
	}
}

func TestSecretPayloadLimitDoesNotExpandOtherOperations(t *testing.T) {
	o := testOptions(t)
	startBroker(t, o)
	if err := testClient(o).call(context.Background(), ActionNotify, NotificationRequest{Summary: strings.Repeat("x", 17<<10)}); err == nil {
		t.Fatal("larger secret limit expanded desktop requests")
	}
}
