/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestEnrolmentEscalationMatchesTheSession(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if got := enrolmentEscalationTools(); !slices.Equal(got, []string{"sudo", "doas", "pkexec", "run0"}) {
		t.Fatalf("terminal session tries %v", got)
	}
	t.Setenv("DISPLAY", ":0")
	if got := enrolmentEscalationTools(); !slices.Equal(got, []string{"pkexec", "run0", "sudo", "doas"}) {
		t.Fatalf("graphical session tries %v", got)
	}
}

func TestPrivilegedEnrolmentRequestRecordsTheExactEvidence(t *testing.T) {
	ledger := testAnchorLedger(t)
	anchor := testAnchor()
	signed := testSignedState(2)
	acceptSignaturesOf(t, anchor.Origin)
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(socketRequest{Action: anchorEnrolAction, Anchor: &anchor, Signature: signed}); err != nil {
		t.Fatal(err)
	}
	if err := runEnrolmentRequest(&input, ledger); err != nil {
		t.Fatal(err)
	}
	recorded, found, err := ledger.Recorded(anchor.UID, anchor.Origin)
	if err != nil || !found {
		t.Fatalf("the privileged request was not recorded: %v, %v", found, err)
	}
	if recorded.Signature == nil || !bytes.Equal(recorded.Signature.Bundle, signed.Bundle) {
		t.Fatalf("the privileged request recorded %+v", recorded.Signature)
	}
}

func TestPrivilegedEnrolmentRequestRejectsAnotherAction(t *testing.T) {
	input := bytes.NewBufferString(`{"action":"clear-removal"}`)
	if err := runEnrolmentRequest(input, testAnchorLedger(t)); err == nil {
		t.Fatal("the privileged enrolment entry point accepted another action")
	}
}

func TestPrivilegedEnrolmentRequestRejectsUnknownInput(t *testing.T) {
	input := bytes.NewBufferString(`{"action":"enrol","unexpected":true}`)
	if err := runEnrolmentRequest(input, testAnchorLedger(t)); err == nil {
		t.Fatal("the privileged enrolment entry point accepted an unknown field")
	}
}
