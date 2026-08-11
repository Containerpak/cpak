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
	if request.appName != "Bottles" || request.summary != "Update ready" || request.body != "Soda can be updated." {
		t.Fatalf("notification: %+v", request)
	}
	if request.expireTimeout != 5000 || request.icon != "com.usebottles.bottles" {
		t.Fatalf("notification options: %+v", request)
	}
	if urgency, ok := request.hints["urgency"]; !ok || urgency.Value() != byte(2) {
		t.Fatalf("notification urgency: %v", urgency)
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
