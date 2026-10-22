/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package gvariant

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// The vectors come from the serialisation examples of the GVariant
// specification, so they hold the encoder and the decoder to the format and
// not merely to each other.
func TestSpecificationVectors(t *testing.T) {
	cases := []struct {
		signature string
		value     any
		data      []byte
	}{
		{"s", "hello world", append([]byte("hello world"), 0)},
		{"ms", "hello world", append([]byte("hello world"), 0, 0)},
		{"ab", []any{true, false, false, true, true}, []byte{1, 0, 0, 1, 1}},
		{"as", []any{"i", "can", "has", "strings?"}, []byte{
			'i', 0, 'c', 'a', 'n', 0, 'h', 'a', 's', 0, 's', 't', 'r', 'i', 'n', 'g', 's', '?', 0,
			0x02, 0x06, 0x0a, 0x13,
		}},
		{"(si)", []any{"foo", int32(-1)}, []byte{'f', 'o', 'o', 0, 0xff, 0xff, 0xff, 0xff, 0x04}},
		{"{si}", []any{"a key", int32(514)}, []byte{'a', ' ', 'k', 'e', 'y', 0, 0, 0, 0x02, 0x02, 0, 0, 0x06}},
		{"a{si}", []any{[]any{"a key", int32(514)}}, []byte{'a', ' ', 'k', 'e', 'y', 0, 0, 0, 0x02, 0x02, 0, 0, 0x06, 0x0d}},
		{"(yi)", []any{byte(0x70), int32(96)}, []byte{0x70, 0, 0, 0, 0x60, 0, 0, 0}},
		{"as", []any{}, []byte{}},
		{"()", []any{}, []byte{0}},
		{"v", Variant{Signature: "(yi)", Value: []any{byte(0x70), int32(96)}}, []byte{0x70, 0, 0, 0, 0x60, 0, 0, 0, 0, '(', 'y', 'i', ')'}},
	}
	for _, test := range cases {
		encoded, err := Encode(test.signature, test.value)
		if err != nil {
			t.Fatalf("Encode(%s): %v", test.signature, err)
		}
		if !bytes.Equal(encoded, test.data) {
			t.Fatalf("Encode(%s) = %x, the specification serialises it as %x", test.signature, encoded, test.data)
		}
		decoded, err := Parse(test.signature, test.data)
		if err != nil {
			t.Fatalf("Parse(%s): %v", test.signature, err)
		}
		if !reflect.DeepEqual(decoded, test.value) {
			t.Fatalf("Parse(%s) = %#v, want %#v", test.signature, decoded, test.value)
		}
	}
}

// The OSTree commit type mixes every container kind, a variant dictionary and
// two framed byte arrays, which makes it the round trip that matters.
func TestCommitShapedValueRoundTrips(t *testing.T) {
	checksum := bytes.Repeat([]byte{0xab}, 32)
	value := []any{
		[]any{
			[]any{"xa.ref", Variant{Signature: "s", Value: "app/org.example.App/x86_64/stable"}},
			[]any{"ostree.ref-binding", Variant{Signature: "as", Value: []any{"app/org.example.App/x86_64/stable"}}},
		},
		[]byte{},
		[]any{[]any{"related", checksum}},
		"Export org.example.App",
		"",
		uint64(0x0102030405060708),
		checksum,
		bytes.Repeat([]byte{0xcd}, 32),
	}
	encoded, err := Encode("(a{sv}aya(say)sstayay)", value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse("(a{sv}aya(say)sstayay)", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatalf("round trip changed the commit:\n got %#v\nwant %#v", decoded, value)
	}
	dict, err := VariantDict(decoded.([]any)[0])
	if err != nil {
		t.Fatal(err)
	}
	if dict["xa.ref"].Value != "app/org.example.App/x86_64/stable" {
		t.Fatalf("metadata lost its ref: %#v", dict)
	}
}

// A container larger than 255 bytes moves to two-byte framing offsets, and a
// decoder that kept reading one byte would take the wrong element bounds.
func TestWideFramingOffsets(t *testing.T) {
	long := string(bytes.Repeat([]byte("x"), 300))
	value := []any{long, "short", long}
	encoded, err := Encode("as", value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Parse("as", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatal("two-byte framing did not survive a round trip")
	}
}

func TestMalformedDataIsRefused(t *testing.T) {
	cases := []struct {
		signature string
		data      []byte
	}{
		{"s", []byte("unterminated")},
		{"s", []byte{'a', 0, 'b', 0}},
		{"u", []byte{1, 2}},
		{"as", []byte{'a', 0, 0x40}},
		{"(si)", []byte{'f', 'o', 'o', 0, 0xff, 0xff, 0xff, 0xff, 0x09}},
		{"v", []byte{1, 2, 3}},
		{"ai", []byte{1, 2, 3}},
	}
	for _, test := range cases {
		if _, err := Parse(test.signature, test.data); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Parse(%s, %x) = %v, want ErrMalformed", test.signature, test.data, err)
		}
	}
}

func TestUnsupportedTypesAreRefused(t *testing.T) {
	for _, signature := range []string{"", "z", "a", "(s", "{s}", "ss"} {
		if _, err := Parse(signature, nil); err == nil {
			t.Fatalf("Parse accepted the type %q", signature)
		}
	}
}
