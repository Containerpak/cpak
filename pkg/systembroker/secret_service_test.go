/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSecretServiceWithPrivateKeyring(t *testing.T) {
	if os.Getenv("CPAK_TEST_PRIVATE_KEYRING") != "1" {
		t.Skip("requires an isolated Secret Service session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	attrs := map[string]string{"context": "synthetic", "account": "one"}
	a, b := "github.com/test/one", "github.com/test/two"
	clients := map[string]Client{}
	for _, origin := range []string{a, b} {
		o := testOptions(t)
		o.SecretOrigin = origin
		o.SecretCapabilities = map[string]bool{"read-owned": true, "write-owned": true}
		startBroker(t, o)
		clients[origin] = testClient(o)
	}
	for _, origin := range []string{a, b} {
		if _, err := clients[origin].Secrets(ctx, SecretRequest{Operation: "put", Attributes: attrs, Value: origin}); err != nil {
			t.Fatal(err)
		}
	}
	for _, origin := range []string{a, b} {
		r, err := clients[origin].Secrets(ctx, SecretRequest{Operation: "get", Attributes: attrs})
		if err != nil || len(r.Entries) != 1 || r.Entries[0].Value != origin {
			t.Fatalf("application isolation: %v", err)
		}
	}
	r, err := clients[a].Secrets(ctx, SecretRequest{Operation: "search", Attributes: map[string]string{"context": "synthetic"}})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Attributes["account"] != "one" {
		t.Fatalf("scoped account enumeration: %v", err)
	}
	if _, err := clients[a].Secrets(ctx, SecretRequest{Operation: "delete", Attributes: attrs}); err != nil {
		t.Fatal(err)
	}
	r, err = clients[b].Secrets(ctx, SecretRequest{Operation: "get", Attributes: attrs})
	if err != nil || len(r.Entries) != 1 || r.Entries[0].Value != b {
		t.Fatal("deleting one application's entry changed another's")
	}
	r, err = clients[a].Secrets(ctx, SecretRequest{Operation: "get", Attributes: attrs})
	if err != nil || len(r.Entries) != 0 {
		t.Fatal("deleted credential remained available")
	}
	if _, err := clients[b].Secrets(ctx, SecretRequest{Operation: "delete", Attributes: attrs}); err != nil {
		t.Fatal(err)
	}
}
