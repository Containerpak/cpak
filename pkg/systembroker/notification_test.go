/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import "testing"

func TestParseNotification(t *testing.T) {
	request, err := parseNotification([]string{
		"--app-name", "Bottles",
		"--urgency=critical",
		"--expire-time", "5000",
		"--icon", "com.usebottles.bottles",
		"Update ready",
		"Soda can be updated.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.AppName != "Bottles" || request.Summary != "Update ready" || request.Body != "Soda can be updated." {
		t.Fatalf("notification: %+v", request)
	}
	if request.ExpireTimeout != 5000 || request.Icon != "com.usebottles.bottles" {
		t.Fatalf("notification options: %+v", request)
	}
	if request.Urgency != "critical" {
		t.Fatalf("notification urgency: %s", request.Urgency)
	}
}

func TestParseNotificationRejectsUnsupportedOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--action", "open=Open", "Summary"},
		{"--expire-time", "invalid", "Summary"},
		{"--urgency", "unknown", "Summary"},
		{"Summary", "Body", "Extra"},
	} {
		if _, err := parseNotification(args); err == nil {
			t.Fatalf("notification arguments were accepted: %v", args)
		}
	}
}
