/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
)

const (
	secretDestination = "org.freedesktop.secrets"
	secretService     = "org.freedesktop.Secret.Service"
	secretItem        = "org.freedesktop.Secret.Item"
	secretPrompt      = "org.freedesktop.Secret.Prompt"
	secretRoot        = dbus.ObjectPath("/org/freedesktop/secrets")
	secretCollection  = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
)

type secretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func secretServiceRequest(ctx context.Context, origin string, r SecretRequest) (SecretResult, error) {
	c, err := dbus.ConnectSessionBus()
	if err != nil {
		return SecretResult{}, err
	}
	defer c.Close()
	s := c.Object(secretDestination, secretRoot)
	var output dbus.Variant
	var session dbus.ObjectPath
	if err = s.CallWithContext(ctx, secretService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&output, &session); err != nil {
		return SecretResult{}, err
	}
	if !session.IsValid() || session == "/" {
		return SecretResult{}, errors.New("invalid secret session")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.Object(secretDestination, session).CallWithContext(cleanup, "org.freedesktop.Secret.Session.Close", 0)
	}()
	attributes := scopedSecretAttributes(origin, r.Attributes)
	if r.Operation == "put" {
		return putSecret(ctx, c, session, attributes, r.Value)
	}
	var unlocked, locked []dbus.ObjectPath
	if err = s.CallWithContext(ctx, secretService+".SearchItems", 0, attributes).Store(&unlocked, &locked); err != nil {
		return SecretResult{}, err
	}
	if len(unlocked)+len(locked) > 128 {
		return SecretResult{}, errors.New("too many secrets")
	}
	if len(locked) > 0 {
		if err = unlockSecrets(ctx, c, locked); err != nil {
			return SecretResult{}, err
		}
	}
	items := append(unlocked, locked...)
	if r.Operation == "get" && len(items) > 1 {
		return SecretResult{}, errors.New("ambiguous secret lookup")
	}
	result := SecretResult{Entries: []SecretEntry{}}
	for _, path := range items {
		if !path.IsValid() || path == "/" {
			return SecretResult{}, errors.New("invalid secret item")
		}
		item := c.Object(secretDestination, path)
		var properties dbus.Variant
		if err = item.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, secretItem, "Attributes").Store(&properties); err != nil {
			return SecretResult{}, err
		}
		stored, ok := properties.Value().(map[string]string)
		if !ok || !secretAttributesMatch(stored, attributes) {
			return SecretResult{}, errors.New("secret item scope mismatch")
		}
		if r.Operation == "delete" {
			var prompt dbus.ObjectPath
			if err = item.CallWithContext(ctx, secretItem+".Delete", 0).Store(&prompt); err != nil {
				return SecretResult{}, err
			}
			if err = waitSecretPrompt(ctx, c, prompt); err != nil {
				return SecretResult{}, err
			}
			result.Changed = true
			continue
		}
		var value secretValue
		if err = item.CallWithContext(ctx, secretItem+".GetSecret", 0, session).Store(&value); err != nil {
			return SecretResult{}, err
		}
		if value.Session != session || len(value.Value) > maxSecretSize || !utf8.Valid(value.Value) {
			clear(value.Value)
			return SecretResult{}, errors.New("invalid stored secret")
		}
		delete(stored, "cpak.origin")
		delete(stored, "xdg:schema")
		result.Entries = append(result.Entries, SecretEntry{Attributes: stored, Value: string(value.Value)})
		clear(value.Value)
	}
	return result, nil
}

func secretAttributesMatch(stored, requested map[string]string) bool {
	for k, v := range requested {
		if value, ok := stored[k]; !ok || value != v {
			return false
		}
	}
	return true
}

func putSecret(ctx context.Context, c *dbus.Conn, session dbus.ObjectPath, attributes map[string]string, value string) (SecretResult, error) {
	if err := unlockSecrets(ctx, c, []dbus.ObjectPath{secretCollection}); err != nil {
		return SecretResult{}, err
	}
	properties := map[string]dbus.Variant{
		secretItem + ".Label":      dbus.MakeVariant("cpak application credential"),
		secretItem + ".Attributes": dbus.MakeVariant(attributes),
	}
	secret := secretValue{Session: session, Value: []byte(value), ContentType: "text/plain; charset=utf8"}
	defer clear(secret.Value)
	var item, prompt dbus.ObjectPath
	if err := c.Object(secretDestination, secretCollection).CallWithContext(ctx, "org.freedesktop.Secret.Collection.CreateItem", 0, properties, secret, true).Store(&item, &prompt); err != nil {
		return SecretResult{}, err
	}
	if err := waitSecretPrompt(ctx, c, prompt); err != nil {
		return SecretResult{}, err
	}
	return SecretResult{Entries: []SecretEntry{}, Changed: true}, nil
}

func unlockSecrets(ctx context.Context, c *dbus.Conn, items []dbus.ObjectPath) error {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := c.Object(secretDestination, secretRoot).CallWithContext(ctx, secretService+".Unlock", 0, items).Store(&unlocked, &prompt); err != nil {
		return err
	}
	return waitSecretPrompt(ctx, c, prompt)
}

func waitSecretPrompt(ctx context.Context, c *dbus.Conn, path dbus.ObjectPath) error {
	if path == "/" {
		return nil
	}
	if !path.IsValid() {
		return errors.New("invalid secret prompt")
	}
	signals := make(chan *dbus.Signal, 1)
	c.Signal(signals)
	defer c.RemoveSignal(signals)
	var owner string
	if err := c.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, secretDestination).Store(&owner); err != nil {
		return err
	}
	match := "type='signal',sender='" + secretDestination + "',path='" + string(path) + "',interface='" + secretPrompt + "',member='Completed'"
	if err := c.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.AddMatch", 0, match).Err; err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		c.BusObject().CallWithContext(cleanup, "org.freedesktop.DBus.RemoveMatch", 0, match)
	}()
	if err := c.Object(secretDestination, path).CallWithContext(ctx, secretPrompt+".Prompt", 0, "").Err; err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal := <-signals:
			if signal == nil || signal.Sender != owner || signal.Path != path || signal.Name != secretPrompt+".Completed" || len(signal.Body) != 2 {
				continue
			}
			dismissed, ok := signal.Body[0].(bool)
			if !ok || dismissed {
				return errors.New("secret prompt was dismissed")
			}
			return nil
		}
	}
}
