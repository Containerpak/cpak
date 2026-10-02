/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDesktopCallbackReturnsToTheOpeningInstance(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}

	instance, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app")
	if err != nil {
		t.Fatal(err)
	}
	if !found || instance != "office-test" {
		t.Fatalf("desktop callback target: found %t, instance %q", found, instance)
	}
	if _, found, err = ResolveDesktopCallback(directory, callback, "github.com/example/app"); err != nil || found {
		t.Fatalf("desktop callback was not consumed: found %t, error %v", found, err)
	}
}

func TestDesktopCallbackRecordDoesNotStoreOAuthValues(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	state := "secret-state-" + strings.Repeat("s", 32)
	authorization, _ := desktopCallbackPair(state)
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("callback records: %d", len(entries))
	}
	content, err := os.ReadFile(filepath.Join(directory, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{state, authorization, "authorization-code", "redirect_uri"} {
		if strings.Contains(string(content), secret) || strings.Contains(entries[0].Name(), secret) {
			t.Fatalf("callback record contains OAuth value %q", secret)
		}
	}
}

func TestDesktopCallbackCannotBeConsumedByAnotherPackage(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}

	if _, found, err := ResolveDesktopCallback(directory, callback, "github.com/other/app"); err != nil || found {
		t.Fatalf("foreign package consumed callback: found %t, error %v", found, err)
	}
	instance, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app")
	if err != nil || !found || instance != "office-test" {
		t.Fatalf("callback owner lost its target: found %t, instance %q, error %v", found, instance, err)
	}
}

func TestDesktopCallbackCannotBeOverwrittenByAnotherInstance(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-first"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-second"); err == nil {
		t.Fatal("registered callback was overwritten")
	}

	instance, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app")
	if err != nil || !found || instance != "office-first" {
		t.Fatalf("first callback target: found %t, instance %q, error %v", found, instance, err)
	}
}

func TestDesktopCallbackCanOnlyBeConsumedOnceConcurrently(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan bool, 2)
	errors := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app")
			results <- found
			errors <- err
		}()
	}
	ready.Wait()
	close(start)
	found := 0
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if <-results {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("callback consumed %d times", found)
	}
}

func TestDesktopCallbackRejectsUncorrelatedURIs(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	redirect := url.QueryEscape("ms-appx-web://microsoft.aad.brokerplugin/client")
	authorization := "https://login.example/authorize?redirect_uri=" + redirect + "&state=short"
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(directory); !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("uncorrelated callback was registered: entries %d, error %v", len(entries), err)
	}
}

func TestDesktopCallbackAcceptsPathBasedCustomSchemes(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	state := "s" + strings.Repeat("t", 42)
	redirect := "com.example.application:/oauth/callback"
	authorization := "https://login.example/authorize?redirect_uri=" + url.QueryEscape(redirect) + "&state=" + state
	callback := redirect + "?code=authorization-code&state=" + state
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}
	instance, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app")
	if err != nil || !found || instance != "office-test" {
		t.Fatalf("path callback target: found %t, instance %q, error %v", found, instance, err)
	}
}

func TestDesktopCallbackExpires(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	now := time.Unix(1000, 0)
	desktopCallbackNow = func() time.Time { return now }
	t.Cleanup(func() { desktopCallbackNow = time.Now })
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}
	desktopCallbackNow = func() time.Time { return now.Add(desktopCallbackLifetime + time.Second) }

	if _, found, err := ResolveDesktopCallback(directory, callback, "github.com/example/app"); err != nil || found {
		t.Fatalf("expired callback remained active: found %t, error %v", found, err)
	}
}

func TestDesktopCallbackLimitsPendingRequests(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	for index := range maxDesktopCallbacks {
		state := fmt.Sprintf("state-%04d-%s", index, strings.Repeat("s", 32))
		authorization, _ := desktopCallbackPair(state)
		if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
			t.Fatalf("register callback %d: %v", index, err)
		}
	}
	authorization, _ := desktopCallbackPair("overflow-" + strings.Repeat("s", 32))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err == nil {
		t.Fatal("pending callback limit was not enforced")
	}
}

func TestDesktopCallbackRefusesAReplacedRecord(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "callbacks")
	authorization, callback := desktopCallbackPair("s" + strings.Repeat("t", 42))
	if err := RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}
	key, ok := responseCallbackKey(callback)
	if !ok {
		t.Fatal("callback key was not accepted")
	}
	path := desktopCallbackPath(directory, key)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ResolveDesktopCallback(directory, callback, "github.com/example/app"); err == nil {
		t.Fatal("replaced desktop callback record was accepted")
	}
}

func desktopCallbackPair(state string) (string, string) {
	redirect := "ms-appx-web://microsoft.aad.brokerplugin/client"
	authorization := "https://login.example/authorize?redirect_uri=" + url.QueryEscape(redirect) + "&state=" + url.QueryEscape(state)
	callback := redirect + "?code=authorization-code&state=" + url.QueryEscape(state)
	return authorization, callback
}
