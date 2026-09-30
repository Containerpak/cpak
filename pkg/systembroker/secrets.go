/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const maxSecretSize = 64 << 10

type SecretRequest struct {
	Operation  string            `json:"operation"`
	Attributes map[string]string `json:"attributes"`
	Value      string            `json:"value,omitempty"`
}

type SecretEntry struct {
	Attributes map[string]string `json:"attributes"`
	Value      string            `json:"value"`
}

type SecretResult struct {
	Entries []SecretEntry `json:"entries"`
	Changed bool          `json:"changed"`
}

func validateSecretRequest(r SecretRequest) error {
	switch r.Operation {
	case "get", "search", "delete":
		if r.Value != "" {
			return errors.New("secret value is only valid for put")
		}
	case "put":
		if len(r.Value) == 0 || len(r.Value) > maxSecretSize {
			return errors.New("invalid secret value size")
		}
	default:
		return errors.New("unsupported secret operation")
	}
	if len(r.Attributes) == 0 || len(r.Attributes) > 16 {
		return errors.New("invalid secret attributes")
	}
	for k, v := range r.Attributes {
		if k == "" || len(k) > 64 || len(v) > 4096 || strings.ContainsAny(k+v, "\x00\r\n") || strings.HasPrefix(k, "cpak.") || k == "xdg:schema" {
			return errors.New("invalid secret attribute")
		}
	}
	return nil
}

func scopedSecretAttributes(origin string, attributes map[string]string) map[string]string {
	digest := sha256.Sum256([]byte(origin))
	result := map[string]string{
		"cpak.origin": hex.EncodeToString(digest[:]),
		"xdg:schema":  "com.containerpak.AppSecrets",
	}
	for k, v := range attributes {
		result[k] = v
	}
	return result
}

func executeSecrets(ctx context.Context, payload []byte, o Options, w *frameWriter) (int, error) {
	var r SecretRequest
	if err := decodePayload(payload, &r); err != nil {
		return 0, errors.New("invalid secret request")
	}
	if err := validateSecretRequest(r); err != nil {
		return 0, err
	}
	capability := "read-owned"
	if r.Operation == "put" || r.Operation == "delete" {
		capability = "write-owned"
	}
	if o.SecretOrigin == "" || !o.SecretCapabilities[capability] {
		return 0, errors.New("secret operation is not permitted")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	backend := o.Secrets
	if backend == nil {
		backend = secretServiceRequest
	}
	result, err := backend(ctx, o.SecretOrigin, r)
	if err != nil {
		// Keyring errors can contain item attributes or values.
		return 0, errors.New("secure credential storage is unavailable or locked")
	}
	if len(result.Entries) > 128 {
		return 0, errors.New("too many stored secrets")
	}
	for _, entry := range result.Entries {
		if len(entry.Value) > maxSecretSize {
			return 0, errors.New("invalid stored secret size")
		}
		for k, v := range r.Attributes {
			if value, ok := entry.Attributes[k]; !ok || value != v {
				return 0, errors.New("stored secret scope mismatch")
			}
		}
	}
	return 0, json.NewEncoder(w.stdout()).Encode(result)
}
