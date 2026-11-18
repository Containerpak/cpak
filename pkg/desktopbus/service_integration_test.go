/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type serviceCall struct {
	sender dbus.Sender
	serial uint32
}

type echoService struct {
	calls chan serviceCall
	reply chan struct{}
}

func (s *echoService) Echo(sender dbus.Sender, message dbus.Message, value string) (string, *dbus.Error) {
	s.calls <- serviceCall{sender: sender, serial: message.Serial()}
	<-s.reply
	return value, nil
}

func TestServiceRepliesWithTwoBusCallers(t *testing.T) {
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "bus.sock")
	upstream := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=unix:path="+upstreamPath)
	if err := upstream.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Process.Kill(); _ = upstream.Wait() })
	waitForPath(t, upstreamPath)
	proxyPath := filepath.Join(directory, "proxy.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_ = Serve(ctx, Options{SocketPath: proxyPath, UpstreamAddress: "unix:path=" + upstreamPath,
			Policy: types.DBusPolicy{Own: []string{"org.example.CpakService"}},
		})
	}()
	waitForPath(t, proxyPath)
	service, err := dbus.Connect("unix:path="+proxyPath, dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler := &echoService{calls: make(chan serviceCall, 2), reply: make(chan struct{})}
	defer close(handler.reply)
	if err := service.Export(handler, "/Service", "org.example.CpakService"); err != nil {
		t.Fatal(err)
	}
	if reply, err := service.RequestName("org.example.CpakService", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request declared service name: %v %v", reply, err)
	}
	watcher, err := dbus.Connect("unix:path="+upstreamPath, dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if _, err := watcher.RequestName("org.example.Unrelated", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	signals := make(chan *dbus.Signal, 2)
	watcher.Signal(signals)
	if err := watcher.AddMatchSignal(dbus.WithMatchInterface("org.example.CpakService")); err != nil {
		t.Fatal(err)
	}
	message := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/Service")),
		dbus.FieldInterface:   dbus.MakeVariant("org.example.CpakService"),
		dbus.FieldMember:      dbus.MakeVariant("Changed"),
		dbus.FieldDestination: dbus.MakeVariant("org.example.Unrelated"),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf("forbidden")),
	}, Body: []any{"forbidden"}}
	if call := <-service.Send(message, make(chan *dbus.Call, 1)).Done; call.Err != nil {
		t.Fatal(call.Err)
	}
	if err := service.Emit("/Service", "org.example.CpakService.Changed", "broadcast"); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-signals:
		if len(signal.Body) != 1 || signal.Body[0] != "broadcast" {
			t.Fatalf("a directed signal reached an undeclared destination: %+v", signal)
		}
	case <-ctx.Done():
		t.Fatal("the service broadcast did not reach the watcher")
	}
	type result struct {
		value string
		err   error
	}
	results := make(chan result, 2)
	for _, value := range []string{"first", "second"} {
		client, err := dbus.Connect("unix:path="+upstreamPath, dbus.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		go func(value string) {
			var returned string
			err := client.Object("org.example.CpakService", "/Service").CallWithContext(ctx, "org.example.CpakService.Echo", 0, value).Store(&returned)
			results <- result{value: returned, err: err}
		}(value)
	}
	var calls []serviceCall
	for len(calls) < 2 {
		select {
		case call := <-handler.calls:
			calls = append(calls, call)
		case <-ctx.Done():
			t.Fatal("both callers did not reach the published service")
		}
	}
	if calls[0].serial != calls[1].serial || calls[0].sender == calls[1].sender {
		t.Fatalf("fixture did not produce independent callers with the same serial: %+v", calls)
	}
	handler.reply <- struct{}{}
	handler.reply <- struct{}{}
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			seen[result.value] = true
		case <-ctx.Done():
			t.Fatal("a caller did not receive its reply")
		}
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("unexpected replies: %v", seen)
	}
}
