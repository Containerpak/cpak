/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package registryauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	secretServiceName      = "org.freedesktop.secrets"
	secretServicePath      = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceInterface = "org.freedesktop.Secret.Service"
	secretCollectionPath   = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	secretCollection       = "org.freedesktop.Secret.Collection"
	secretItem             = "org.freedesktop.Secret.Item"
	secretPrompt           = "org.freedesktop.Secret.Prompt"
	secretSession          = "org.freedesktop.Secret.Session"
	nullObjectPath         = dbus.ObjectPath("/")
)

type secretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type secretService struct {
	connection *dbus.Conn
	session    dbus.ObjectPath
}

func storeSecret(ctx context.Context, record Record, secret string) error {
	service, err := openSecretService(ctx)
	if err != nil {
		return err
	}
	defer service.close()
	if err = service.unlock(ctx, []dbus.ObjectPath{secretCollectionPath}); err != nil {
		return err
	}
	properties := map[string]dbus.Variant{
		secretItem + ".Label":      dbus.MakeVariant("cpak package access"),
		secretItem + ".Attributes": dbus.MakeVariant(secretAttributes(record)),
	}
	value := secretValue{Session: service.session, Value: []byte(secret), ContentType: "text/plain; charset=utf8"}
	var item, prompt dbus.ObjectPath
	call := service.connection.Object(secretServiceName, secretCollectionPath).CallWithContext(
		ctx,
		secretCollection+".CreateItem",
		0,
		properties,
		value,
		true,
	)
	if err = call.Store(&item, &prompt); err != nil {
		return fmt.Errorf("registryauth: store secret: %w", err)
	}
	if err = service.waitPrompt(ctx, prompt); err != nil {
		return fmt.Errorf("registryauth: store secret: %w", err)
	}
	return nil
}

func lookupSecret(ctx context.Context, record Record) (string, error) {
	service, err := openSecretService(ctx)
	if err != nil {
		return "", err
	}
	defer service.close()
	unlocked, locked, err := service.search(ctx, record)
	if err != nil {
		return "", err
	}
	if len(unlocked) == 0 && len(locked) > 0 {
		if err = service.unlock(ctx, locked); err != nil {
			return "", err
		}
		unlocked, _, err = service.search(ctx, record)
		if err != nil {
			return "", err
		}
	}
	if len(unlocked) == 0 {
		return "", errors.New("registryauth: credential is unavailable")
	}
	var value secretValue
	call := service.connection.Object(secretServiceName, unlocked[0]).CallWithContext(
		ctx,
		secretItem+".GetSecret",
		0,
		service.session,
	)
	if err = call.Store(&value); err != nil {
		return "", fmt.Errorf("registryauth: read secret: %w", err)
	}
	if len(value.Value) == 0 {
		return "", errors.New("registryauth: credential is empty")
	}
	return string(value.Value), nil
}

func clearSecret(ctx context.Context, record Record) error {
	service, err := openSecretService(ctx)
	if err != nil {
		return err
	}
	defer service.close()
	unlocked, locked, err := service.search(ctx, record)
	if err != nil {
		return err
	}
	items := append(unlocked, locked...)
	if len(locked) > 0 {
		if err = service.unlock(ctx, locked); err != nil {
			return err
		}
	}
	for _, item := range items {
		var prompt dbus.ObjectPath
		call := service.connection.Object(secretServiceName, item).CallWithContext(ctx, secretItem+".Delete", 0)
		if err = call.Store(&prompt); err != nil {
			return fmt.Errorf("registryauth: clear secret: %w", err)
		}
		if err = service.waitPrompt(ctx, prompt); err != nil {
			return fmt.Errorf("registryauth: clear secret: %w", err)
		}
	}
	return nil
}

func openSecretService(ctx context.Context) (*secretService, error) {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("registryauth: connect to Secret Service: %w", err)
	}
	var output dbus.Variant
	var session dbus.ObjectPath
	call := connection.Object(secretServiceName, secretServicePath).CallWithContext(
		ctx,
		secretServiceInterface+".OpenSession",
		0,
		"plain",
		dbus.MakeVariant(""),
	)
	if err = call.Store(&output, &session); err != nil {
		connection.Close()
		return nil, fmt.Errorf("registryauth: open Secret Service session: %w", err)
	}
	if !session.IsValid() || session == nullObjectPath {
		connection.Close()
		return nil, errors.New("registryauth: Secret Service returned an invalid session")
	}
	return &secretService{connection: connection, session: session}, nil
}

func (s *secretService) close() {
	_ = s.connection.Object(secretServiceName, s.session).Call(secretSession+".Close", 0).Err
	s.connection.Close()
}

func (s *secretService) search(ctx context.Context, record Record) ([]dbus.ObjectPath, []dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	call := s.connection.Object(secretServiceName, secretServicePath).CallWithContext(
		ctx,
		secretServiceInterface+".SearchItems",
		0,
		secretAttributes(record),
	)
	if err := call.Store(&unlocked, &locked); err != nil {
		return nil, nil, fmt.Errorf("registryauth: search Secret Service: %w", err)
	}
	return unlocked, locked, nil
}

func (s *secretService) unlock(ctx context.Context, objects []dbus.ObjectPath) error {
	if len(objects) == 0 {
		return nil
	}
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	call := s.connection.Object(secretServiceName, secretServicePath).CallWithContext(
		ctx,
		secretServiceInterface+".Unlock",
		0,
		objects,
	)
	if err := call.Store(&unlocked, &prompt); err != nil {
		return fmt.Errorf("registryauth: unlock Secret Service: %w", err)
	}
	if err := s.waitPrompt(ctx, prompt); err != nil {
		return fmt.Errorf("registryauth: unlock Secret Service: %w", err)
	}
	return nil
}

func (s *secretService) waitPrompt(ctx context.Context, path dbus.ObjectPath) error {
	if path == "" || path == nullObjectPath {
		return nil
	}
	if !path.IsValid() {
		return errors.New("Secret Service returned an invalid prompt")
	}
	signals := make(chan *dbus.Signal, 1)
	s.connection.Signal(signals)
	defer s.connection.RemoveSignal(signals)
	match := "type='signal',path='" + string(path) + "',interface='" + secretPrompt + "',member='Completed'"
	if err := s.connection.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.AddMatch", 0, match).Err; err != nil {
		return err
	}
	defer s.connection.BusObject().Call("org.freedesktop.DBus.RemoveMatch", 0, match)
	if err := s.connection.Object(secretServiceName, path).CallWithContext(ctx, secretPrompt+".Prompt", 0, "").Err; err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case signal := <-signals:
			if signal == nil || signal.Path != path || signal.Name != secretPrompt+".Completed" || len(signal.Body) < 1 {
				continue
			}
			dismissed, ok := signal.Body[0].(bool)
			if !ok {
				return errors.New("Secret Service returned an invalid prompt response")
			}
			if dismissed {
				return errors.New("Secret Service prompt was dismissed")
			}
			return nil
		}
	}
}
