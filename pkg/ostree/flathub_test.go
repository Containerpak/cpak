/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ostree

import (
	"os"
	"slices"
	"testing"
	"time"
)

// The fixtures are a summary index Flathub published, the signature it
// published beside it and the key its .flatpakrepo carries, so the verifier is
// held to what a real repository signs and not only to what the fixture
// builder writes.
func TestFlathubSummaryIndexSignatureVerifies(t *testing.T) {
	key, err := os.ReadFile("testdata/flathub/flathub.gpg")
	if err != nil {
		t.Fatal(err)
	}
	ring, err := ParseKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ring.Fingerprints(), "6E5C05D979C76DAF93C081354184DD4D907A7CAE") {
		t.Fatalf("the Flathub key did not parse to its published fingerprint: %v", ring.Fingerprints())
	}
	index, err := os.ReadFile("testdata/flathub/summary.idx")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/flathub/summary.idx.sig")
	if err != nil {
		t.Fatal(err)
	}
	signatures, err := signaturesFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.VerifyDetached(index, signatures, time.Now()); err != nil {
		t.Fatalf("the Flathub summary index did not verify: %v", err)
	}
	index[len(index)/2] ^= 0x01
	if err := ring.VerifyDetached(index, signatures, time.Now()); err == nil {
		t.Fatal("a summary index changed by one bit still verified")
	}
}
