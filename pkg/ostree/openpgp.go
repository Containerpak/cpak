/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ostree

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"time"
)

// This is the part of OpenPGP (RFC 4880) an OSTree repository needs: version 4
// public keys and detached binary signatures made with RSA or Ed25519. A
// repository is trusted through a key pinned beforehand, so there is no web of
// trust here, no key servers and no packet the verifier does not understand:
// anything outside this subset is refused rather than skipped.

const (
	packetSignature = 2
	packetPublicKey = 6
	packetUserID    = 13
	packetSubkey    = 14

	algorithmRSA         = 1
	algorithmRSASignOnly = 3
	algorithmEdDSA       = 22

	signatureBinary      = 0x00
	signatureSubkeyBound = 0x18

	subpacketCreationTime   = 2
	subpacketExpirationTime = 3
	subpacketIssuer         = 16
	subpacketKeyFlags       = 27
	subpacketIssuerFinger   = 33

	keyFlagSign = 0x02
)

// ed25519OID is the curve identifier GnuPG writes into a legacy EdDSA key.
var ed25519OID = []byte{0x2b, 0x06, 0x01, 0x04, 0x01, 0xda, 0x47, 0x0f, 0x01}

var ErrUntrustedSignature = errors.New("no signature from a trusted key")

type publicKey struct {
	fingerprint []byte
	algorithm   byte
	rsa         *rsa.PublicKey
	ed25519     ed25519.PublicKey
	packet      []byte
}

func (k publicKey) keyID() uint64 {
	return binary.BigEndian.Uint64(k.fingerprint[len(k.fingerprint)-8:])
}

// Keyring holds the keys an OSTree remote is trusted through: each primary key
// and every subkey that primary key bound to itself for signing.
type Keyring struct {
	keys []publicKey
}

// ParseKeyring reads one or more binary OpenPGP transferable public keys, the
// form a .flatpakrepo carries base64-encoded in GPGKey.
func ParseKeyring(data []byte) (*Keyring, error) {
	packets, err := readPackets(data)
	if err != nil {
		return nil, err
	}
	keyring := &Keyring{}
	var primary *publicKey
	var subkey *publicKey
	for _, packet := range packets {
		switch packet.tag {
		case packetPublicKey:
			key, err := parsePublicKey(packet.body)
			if err != nil {
				return nil, err
			}
			keyring.keys = append(keyring.keys, key)
			primary = &key
			subkey = nil
		case packetSubkey:
			if primary == nil {
				return nil, errors.New("openpgp: subkey without a primary key")
			}
			key, err := parsePublicKey(packet.body)
			if err != nil {
				// A subkey of a kind this verifier cannot use is not an
				// error for the key as a whole, it just never signs here.
				subkey = nil
				continue
			}
			subkey = &key
		case packetSignature:
			if primary == nil || subkey == nil {
				continue
			}
			sig, err := parseSignature(packet.body)
			if err != nil || sig.kind != signatureSubkeyBound || sig.issuedBy(*primary) != nil {
				continue
			}
			if !sig.canSign() {
				continue
			}
			digest, err := sig.digest(func(h hash.Hash) {
				writeKeyForHash(h, primary.packet)
				writeKeyForHash(h, subkey.packet)
			})
			if err != nil {
				continue
			}
			if primary.verify(sig, digest) == nil {
				keyring.keys = append(keyring.keys, *subkey)
				subkey = nil
			}
		case packetUserID:
			subkey = nil
		}
	}
	if len(keyring.keys) == 0 {
		return nil, errors.New("openpgp: no usable public key")
	}
	return keyring, nil
}

// Fingerprints names the primary keys and bound subkeys in the ring, in
// upper-case hexadecimal, so a user can be shown what they are trusting.
func (k *Keyring) Fingerprints() []string {
	values := make([]string, 0, len(k.keys))
	for _, key := range k.keys {
		values = append(values, strings.ToUpper(hex.EncodeToString(key.fingerprint)))
	}
	return values
}

// VerifyDetached accepts data when at least one of the signatures is a valid
// binary signature made by a key in the ring. Signatures from unknown keys are
// ignored, as OSTree does, since a repository may be signed by several parties.
func (k *Keyring) VerifyDetached(data []byte, signatures [][]byte, now time.Time) error {
	var last error = ErrUntrustedSignature
	for _, encoded := range signatures {
		packets, err := readPackets(encoded)
		if err != nil {
			last = err
			continue
		}
		for _, packet := range packets {
			if packet.tag != packetSignature {
				continue
			}
			sig, err := parseSignature(packet.body)
			if err != nil {
				last = err
				continue
			}
			if sig.kind != signatureBinary {
				continue
			}
			if sig.expired(now) {
				last = errors.New("openpgp: signature has expired")
				continue
			}
			digest, err := sig.digest(func(h hash.Hash) { h.Write(data) })
			if err != nil {
				last = err
				continue
			}
			for _, key := range k.keys {
				if sig.issuedBy(key) != nil {
					continue
				}
				if err := key.verify(sig, digest); err == nil {
					return nil
				} else {
					last = err
				}
			}
		}
	}
	return last
}

type packet struct {
	tag  byte
	body []byte
}

func readPackets(data []byte) ([]packet, error) {
	var packets []packet
	for len(data) > 0 {
		header := data[0]
		if header&0x80 == 0 {
			return nil, errors.New("openpgp: invalid packet header")
		}
		var tag byte
		var length, consumed int
		if header&0x40 != 0 {
			tag = header & 0x3f
			if len(data) < 2 {
				return nil, errors.New("openpgp: truncated packet length")
			}
			first := int(data[1])
			switch {
			case first < 192:
				length, consumed = first, 2
			case first < 224:
				if len(data) < 3 {
					return nil, errors.New("openpgp: truncated packet length")
				}
				length, consumed = (first-192)<<8+int(data[2])+192, 3
			case first == 255:
				if len(data) < 6 {
					return nil, errors.New("openpgp: truncated packet length")
				}
				length, consumed = int(binary.BigEndian.Uint32(data[2:6])), 6
			default:
				return nil, errors.New("openpgp: partial packet lengths are not supported")
			}
		} else {
			tag = (header >> 2) & 0x0f
			switch header & 0x03 {
			case 0:
				if len(data) < 2 {
					return nil, errors.New("openpgp: truncated packet length")
				}
				length, consumed = int(data[1]), 2
			case 1:
				if len(data) < 3 {
					return nil, errors.New("openpgp: truncated packet length")
				}
				length, consumed = int(binary.BigEndian.Uint16(data[1:3])), 3
			case 2:
				if len(data) < 5 {
					return nil, errors.New("openpgp: truncated packet length")
				}
				length, consumed = int(binary.BigEndian.Uint32(data[1:5])), 5
			default:
				return nil, errors.New("openpgp: indeterminate packet lengths are not supported")
			}
		}
		if length < 0 || consumed+length > len(data) {
			return nil, errors.New("openpgp: truncated packet")
		}
		packets = append(packets, packet{tag: tag, body: data[consumed : consumed+length]})
		data = data[consumed+length:]
	}
	return packets, nil
}

func parsePublicKey(body []byte) (publicKey, error) {
	if len(body) < 6 || body[0] != 4 {
		return publicKey{}, errors.New("openpgp: only version 4 keys are supported")
	}
	key := publicKey{algorithm: body[5], packet: body}
	fields := body[6:]
	switch key.algorithm {
	case algorithmRSA, algorithmRSASignOnly:
		modulus, rest, err := readMPI(fields)
		if err != nil {
			return publicKey{}, err
		}
		exponent, _, err := readMPI(rest)
		if err != nil {
			return publicKey{}, err
		}
		e := new(big.Int).SetBytes(exponent)
		if !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31 {
			return publicKey{}, errors.New("openpgp: unsupported RSA exponent")
		}
		n := new(big.Int).SetBytes(modulus)
		if n.BitLen() < 2048 {
			return publicKey{}, errors.New("openpgp: RSA keys shorter than 2048 bits are refused")
		}
		key.rsa = &rsa.PublicKey{N: n, E: int(e.Int64())}
	case algorithmEdDSA:
		if len(fields) < 1 || len(fields) < 1+int(fields[0]) {
			return publicKey{}, errors.New("openpgp: truncated curve identifier")
		}
		oid := fields[1 : 1+int(fields[0])]
		if !bytes.Equal(oid, ed25519OID) {
			return publicKey{}, errors.New("openpgp: unsupported EdDSA curve")
		}
		point, _, err := readMPI(fields[1+len(oid):])
		if err != nil {
			return publicKey{}, err
		}
		if len(point) != 33 || point[0] != 0x40 {
			return publicKey{}, errors.New("openpgp: malformed Ed25519 point")
		}
		key.ed25519 = ed25519.PublicKey(point[1:])
	default:
		return publicKey{}, fmt.Errorf("openpgp: unsupported key algorithm %d", key.algorithm)
	}
	h := sha1.New()
	writeKeyForHash(h, body)
	key.fingerprint = h.Sum(nil)
	return key, nil
}

func writeKeyForHash(h hash.Hash, body []byte) {
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
}

func readMPI(data []byte) (value []byte, rest []byte, err error) {
	if len(data) < 2 {
		return nil, nil, errors.New("openpgp: truncated MPI")
	}
	bits := int(binary.BigEndian.Uint16(data))
	size := (bits + 7) / 8
	if len(data) < 2+size {
		return nil, nil, errors.New("openpgp: truncated MPI")
	}
	return data[2 : 2+size], data[2+size:], nil
}

type signature struct {
	kind       byte
	algorithm  byte
	hash       crypto.Hash
	hashed     []byte // version byte through the end of the hashed subpackets
	left16     []byte
	created    time.Time
	expiresIn  time.Duration
	issuer     uint64
	issuerFull []byte
	keyFlags   byte
	hasFlags   bool
	values     [][]byte
}

func parseSignature(body []byte) (signature, error) {
	if len(body) < 6 || body[0] != 4 {
		return signature{}, errors.New("openpgp: only version 4 signatures are supported")
	}
	sig := signature{kind: body[1], algorithm: body[2]}
	switch body[3] {
	case 8:
		sig.hash = crypto.SHA256
	case 9:
		sig.hash = crypto.SHA384
	case 10:
		sig.hash = crypto.SHA512
	case 11:
		sig.hash = crypto.SHA224
	default:
		// SHA-1 and MD5 can be forged, so signatures made with them are
		// treated as no signature at all.
		return signature{}, fmt.Errorf("openpgp: refused hash algorithm %d", body[3])
	}
	hashedLength := int(binary.BigEndian.Uint16(body[4:6]))
	if len(body) < 6+hashedLength+2 {
		return signature{}, errors.New("openpgp: truncated signature")
	}
	hashedArea := body[6 : 6+hashedLength]
	sig.hashed = body[:6+hashedLength]
	rest := body[6+hashedLength:]
	unhashedLength := int(binary.BigEndian.Uint16(rest))
	if len(rest) < 2+unhashedLength+2 {
		return signature{}, errors.New("openpgp: truncated signature")
	}
	unhashedArea := rest[2 : 2+unhashedLength]
	rest = rest[2+unhashedLength:]
	sig.left16 = rest[:2]
	rest = rest[2:]

	if err := sig.readSubpackets(hashedArea, true); err != nil {
		return signature{}, err
	}
	if err := sig.readSubpackets(unhashedArea, false); err != nil {
		return signature{}, err
	}
	if sig.created.IsZero() {
		return signature{}, errors.New("openpgp: signature has no creation time")
	}

	count := 1
	if sig.algorithm == algorithmEdDSA {
		count = 2
	}
	for range count {
		value, after, err := readMPI(rest)
		if err != nil {
			return signature{}, err
		}
		sig.values = append(sig.values, value)
		rest = after
	}
	return sig, nil
}

func (s *signature) readSubpackets(area []byte, hashed bool) error {
	for len(area) > 0 {
		first := int(area[0])
		var length, consumed int
		switch {
		case first < 192:
			length, consumed = first, 1
		case first < 255:
			if len(area) < 2 {
				return errors.New("openpgp: truncated subpacket")
			}
			length, consumed = (first-192)<<8+int(area[1])+192, 2
		default:
			if len(area) < 5 {
				return errors.New("openpgp: truncated subpacket")
			}
			length, consumed = int(binary.BigEndian.Uint32(area[1:5])), 5
		}
		if length < 1 || consumed+length > len(area) {
			return errors.New("openpgp: truncated subpacket")
		}
		kind := area[consumed] & 0x7f
		critical := area[consumed]&0x80 != 0
		data := area[consumed+1 : consumed+length]
		area = area[consumed+length:]

		switch kind {
		case subpacketCreationTime:
			if hashed && len(data) == 4 {
				s.created = time.Unix(int64(binary.BigEndian.Uint32(data)), 0)
			}
		case subpacketExpirationTime:
			if hashed && len(data) == 4 {
				s.expiresIn = time.Duration(binary.BigEndian.Uint32(data)) * time.Second
			}
		case subpacketIssuer:
			if len(data) == 8 {
				s.issuer = binary.BigEndian.Uint64(data)
			}
		case subpacketIssuerFinger:
			if len(data) == 21 && data[0] == 4 {
				s.issuerFull = data[1:]
			}
		case subpacketKeyFlags:
			if hashed && len(data) > 0 {
				s.keyFlags = data[0]
				s.hasFlags = true
			}
		default:
			if critical && hashed {
				return fmt.Errorf("openpgp: unknown critical subpacket %d", kind)
			}
		}
	}
	return nil
}

func (s signature) canSign() bool {
	return s.hasFlags && s.keyFlags&keyFlagSign != 0
}

func (s signature) expired(now time.Time) bool {
	return s.expiresIn != 0 && now.After(s.created.Add(s.expiresIn))
}

func (s signature) issuedBy(key publicKey) error {
	if s.issuerFull != nil {
		if !bytes.Equal(s.issuerFull, key.fingerprint) {
			return errors.New("openpgp: issued by another key")
		}
		return nil
	}
	if s.issuer != 0 && s.issuer != key.keyID() {
		return errors.New("openpgp: issued by another key")
	}
	if s.issuer == 0 {
		return errors.New("openpgp: signature names no issuer")
	}
	return nil
}

func (s signature) digest(write func(hash.Hash)) ([]byte, error) {
	var h hash.Hash
	switch s.hash {
	case crypto.SHA224:
		h = sha256.New224()
	case crypto.SHA256:
		h = sha256.New()
	case crypto.SHA384:
		h = sha512.New384()
	case crypto.SHA512:
		h = sha512.New()
	default:
		return nil, errors.New("openpgp: unsupported hash")
	}
	write(h)
	h.Write(s.hashed)
	trailer := []byte{4, 0xff, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(trailer[2:], uint32(len(s.hashed)))
	h.Write(trailer)
	digest := h.Sum(nil)
	if !bytes.Equal(digest[:2], s.left16) {
		return nil, errors.New("openpgp: signature does not match the signed data")
	}
	return digest, nil
}

func (k publicKey) verify(sig signature, digest []byte) error {
	if sig.algorithm != k.algorithm && !(k.algorithm == algorithmRSASignOnly && sig.algorithm == algorithmRSA) {
		return errors.New("openpgp: signature algorithm does not match the key")
	}
	switch {
	case k.rsa != nil:
		value := sig.values[0]
		size := (k.rsa.N.BitLen() + 7) / 8
		if len(value) > size {
			return errors.New("openpgp: malformed RSA signature")
		}
		padded := make([]byte, size)
		copy(padded[size-len(value):], value)
		if err := rsa.VerifyPKCS1v15(k.rsa, sig.hash, digest, padded); err != nil {
			return errors.New("openpgp: invalid RSA signature")
		}
		return nil
	case k.ed25519 != nil:
		raw := make([]byte, 64)
		r, s := sig.values[0], sig.values[1]
		if len(r) > 32 || len(s) > 32 {
			return errors.New("openpgp: malformed Ed25519 signature")
		}
		copy(raw[32-len(r):32], r)
		copy(raw[64-len(s):], s)
		if !ed25519.Verify(k.ed25519, digest, raw) {
			return errors.New("openpgp: invalid Ed25519 signature")
		}
		return nil
	}
	return errors.New("openpgp: key cannot verify signatures")
}
