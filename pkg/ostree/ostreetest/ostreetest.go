/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package ostreetest builds signed archive repositories for tests, so the
// client can be exercised against objects laid out exactly as OSTree lays
// them out, including the ways a hostile repository could lay them out.
package ostreetest

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

// Key is an RSA signing key in the form an OSTree remote publishes.
type Key struct {
	private     *rsa.PrivateKey
	packet      []byte
	fingerprint []byte
}

// NewKey generates a signing key. Generation is slow enough that tests should
// share one where they can.
func NewKey() *Key {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return keyFrom(private)
}

func keyFrom(private *rsa.PrivateKey) *Key {
	body := []byte{4, 0x5f, 0, 0, 0, 1}
	body = append(body, mpi(private.N.Bytes())...)
	body = append(body, mpi(big.NewInt(int64(private.E)).Bytes())...)
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
	return &Key{private: private, packet: body, fingerprint: h.Sum(nil)}
}

// Public answers with the transferable public key, the bytes a .flatpakrepo
// carries base64-encoded.
func (k *Key) Public() []byte {
	data := packet(6, k.packet)
	return append(data, packet(13, []byte("cpak test <test@cpak.invalid>"))...)
}

// PublicWithSubkey answers with this key as the primary and sub bound to it
// as a signing subkey, which is how most repository keys are published.
func (k *Key) PublicWithSubkey(sub *Key) []byte {
	data := k.Public()
	data = append(data, packet(14, sub.packet)...)
	hashed := subpackets(
		subpacket(2, binary.BigEndian.AppendUint32(nil, uint32(time.Now().Unix()))),
		subpacket(27, []byte{0x02}),
		subpacket(33, append([]byte{4}, k.fingerprint...)),
	)
	sig := k.signature(0x18, hashed, func(write func([]byte)) {
		write([]byte{0x99, byte(len(k.packet) >> 8), byte(len(k.packet))})
		write(k.packet)
		write([]byte{0x99, byte(len(sub.packet) >> 8), byte(len(sub.packet))})
		write(sub.packet)
	})
	return append(data, sig...)
}

// PublicWithUnboundSubkey answers with sub listed under this key without the
// signature that binds it, which no verifier may take as this key's word.
func (k *Key) PublicWithUnboundSubkey(sub *Key) []byte {
	return append(k.Public(), packet(14, sub.packet)...)
}

// Sign answers with a detached binary signature over data.
func (k *Key) Sign(data []byte) []byte {
	return k.SignAt(data, time.Now(), 0)
}

// SignAt answers with a detached signature made at created, which expires
// after lifetime unless lifetime is zero.
func (k *Key) SignAt(data []byte, created time.Time, lifetime time.Duration) []byte {
	hashed := subpackets(
		subpacket(2, binary.BigEndian.AppendUint32(nil, uint32(created.Unix()))),
		subpacket(33, append([]byte{4}, k.fingerprint...)),
	)
	if lifetime != 0 {
		hashed = append(hashed, subpacket(3, binary.BigEndian.AppendUint32(nil, uint32(lifetime/time.Second)))...)
	}
	return k.signature(0x00, hashed, func(write func([]byte)) { write(data) })
}

func (k *Key) signature(kind byte, hashedArea []byte, signed func(func([]byte))) []byte {
	head := []byte{4, kind, 1, 8, byte(len(hashedArea) >> 8), byte(len(hashedArea))}
	head = append(head, hashedArea...)
	h := sha256.New()
	signed(func(data []byte) { h.Write(data) })
	h.Write(head)
	trailer := []byte{4, 0xff, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(trailer[2:], uint32(len(head)))
	h.Write(trailer)
	digest := h.Sum(nil)
	value, err := rsa.SignPKCS1v15(rand.Reader, k.private, crypto.SHA256, digest)
	if err != nil {
		panic(err)
	}
	body := append([]byte{}, head...)
	body = append(body, 0, 0)
	body = append(body, digest[:2]...)
	body = append(body, mpi(value)...)
	return packet(2, body)
}

func mpi(value []byte) []byte {
	value = bytes.TrimLeft(value, "\x00")
	bits := len(value) * 8
	if len(value) > 0 {
		for mask := byte(0x80); value[0]&mask == 0; mask >>= 1 {
			bits--
		}
	}
	return append([]byte{byte(bits >> 8), byte(bits)}, value...)
}

func packet(tag byte, body []byte) []byte {
	header := []byte{0xc0 | tag}
	switch {
	case len(body) < 192:
		header = append(header, byte(len(body)))
	case len(body) < 8384:
		value := len(body) - 192
		header = append(header, byte(value>>8)+192, byte(value))
	default:
		header = append(header, 0xff)
		header = binary.BigEndian.AppendUint32(header, uint32(len(body)))
	}
	return append(header, body...)
}

func subpacket(kind byte, data []byte) []byte {
	return append([]byte{byte(len(data) + 1), kind}, data...)
}

func subpackets(values ...[]byte) []byte {
	return bytes.Join(values, nil)
}

// File is a regular file, a symbolic link or a directory of a commit tree.
type File struct {
	Content []byte
	Mode    uint32
	Link    string
	Dir     bool
}

// Repo is an archive repository held in memory and served over HTTP.
type Repo struct {
	mu      sync.Mutex
	objects map[string][]byte
	refs    map[string][32]byte
	signers []*Key
	indexed bool
	server  *httptest.Server

	// Override replaces what a path serves, after everything else is built,
	// so a test can play a repository that lies.
	Override map[string][]byte
}

// NewRepo makes a repository whose summaries and commits are signed by the
// given keys. An indexed repository publishes summary.idx and one subsummary
// per architecture, as Flathub does; otherwise it publishes a single summary.
func NewRepo(indexed bool, signers ...*Key) *Repo {
	repo := &Repo{
		objects:  map[string][]byte{},
		refs:     map[string][32]byte{},
		signers:  signers,
		indexed:  indexed,
		Override: map[string][]byte{},
	}
	repo.server = httptest.NewServer(http.HandlerFunc(repo.serve))
	return repo
}

func (r *Repo) URL() string { return r.server.URL + "/" }
func (r *Repo) Close()      { r.server.Close() }

func (r *Repo) serve(w http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := strings.TrimPrefix(request.URL.Path, "/")
	if data, ok := r.Override[name]; ok {
		if data == nil {
			http.NotFound(w, request)
			return
		}
		w.Write(data)
		return
	}
	data, ok := r.objects[name]
	if !ok {
		http.NotFound(w, request)
		return
	}
	w.Write(data)
}

func (r *Repo) put(checksum [32]byte, kind string, data []byte) {
	text := hex.EncodeToString(checksum[:])
	r.objects["objects/"+text[:2]+"/"+text[2:]+"."+kind] = data
}

func (r *Repo) signatures(data []byte) []byte {
	sigs := make([]any, 0, len(r.signers))
	for _, key := range r.signers {
		sigs = append(sigs, key.Sign(data))
	}
	encoded, err := gvariant.Encode("a{sv}", []any{[]any{"ostree.gpgsigs", gvariant.Variant{Signature: "aay", Value: sigs}}})
	if err != nil {
		panic(err)
	}
	return encoded
}

func swap32(value uint32) uint32 {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	return binary.LittleEndian.Uint32(raw[:])
}

func swap64(value uint64) uint64 {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	return binary.LittleEndian.Uint64(raw[:])
}

// FileChecksum answers with the checksum OSTree gives a file object.
func FileChecksum(file File) [32]byte {
	checksum, _ := fileObject(file)
	return checksum
}

func fileObject(file File) ([32]byte, []byte) {
	mode := file.Mode
	if file.Link != "" {
		mode = 0o120777
	} else if mode&0o170000 == 0 {
		mode |= 0o100000
	}
	xattrs := []any{}
	header, err := gvariant.Encode("(uuuusa(ayay))", []any{swap32(0), swap32(0), swap32(mode), swap32(0), file.Link, xattrs})
	if err != nil {
		panic(err)
	}
	prefix := binary.BigEndian.AppendUint32(nil, uint32(len(header)))
	prefix = append(prefix, 0, 0, 0, 0)
	prefix = append(prefix, header...)
	h := sha256.New()
	h.Write(prefix)
	h.Write(file.Content)
	var checksum [32]byte
	copy(checksum[:], h.Sum(nil))

	archive, err := gvariant.Encode("(tuuuusa(ayay))", []any{swap64(uint64(len(file.Content))), swap32(0), swap32(0), swap32(mode), swap32(0), file.Link, xattrs})
	if err != nil {
		panic(err)
	}
	object := binary.BigEndian.AppendUint32(nil, uint32(len(archive)))
	object = append(object, 0, 0, 0, 0)
	object = append(object, archive...)
	var compressed bytes.Buffer
	writer, _ := flate.NewWriter(&compressed, flate.BestSpeed)
	writer.Write(file.Content)
	writer.Close()
	return checksum, append(object, compressed.Bytes()...)
}

func (r *Repo) dirMeta(mode uint32) [32]byte {
	data, err := gvariant.Encode("(uuua(ayay))", []any{swap32(0), swap32(0), swap32(0o040000 | mode), []any{}})
	if err != nil {
		panic(err)
	}
	checksum := sha256.Sum256(data)
	r.put(checksum, "dirmeta", data)
	return checksum
}

// tree writes the dirtree and dirmeta objects for the directory at prefix
// and everything under it.
func (r *Repo) tree(files map[string]File, prefix string) ([32]byte, [32]byte) {
	type child struct {
		name string
		dir  bool
	}
	children := map[string]child{}
	for name, file := range files {
		relative := name
		if prefix != "" {
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			relative = strings.TrimPrefix(name, prefix+"/")
		}
		first, rest, nested := strings.Cut(relative, "/")
		_ = rest
		if nested || file.Dir {
			children[first] = child{name: first, dir: true}
		} else {
			children[first] = child{name: first}
		}
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	fileItems := []any{}
	dirItems := []any{}
	for _, name := range names {
		full := path.Join(prefix, name)
		if children[name].dir {
			tree, meta := r.tree(files, full)
			dirItems = append(dirItems, []any{name, tree[:], meta[:]})
			continue
		}
		checksum, object := fileObject(files[full])
		r.put(checksum, "filez", object)
		fileItems = append(fileItems, []any{name, checksum[:]})
	}
	data, err := gvariant.Encode("(a(say)a(sayay))", []any{fileItems, dirItems})
	if err != nil {
		panic(err)
	}
	checksum := sha256.Sum256(data)
	r.put(checksum, "dirtree", data)
	mode := uint32(0o755)
	if file, ok := files[prefix]; ok && file.Dir && file.Mode != 0 {
		mode = file.Mode
	}
	return checksum, r.dirMeta(mode)
}

// Commit writes a commit of files for ref, publishes it in the summary and
// answers with its checksum. Metadata is added to what OSTree itself writes,
// which includes the ref binding.
func (r *Repo) Commit(ref string, files map[string]File, metadata map[string]gvariant.Variant) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, meta := r.tree(files, "")
	items := []any{[]any{"ostree.ref-binding", gvariant.Variant{Signature: "as", Value: []any{ref}}}}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		items = append(items, []any{key, metadata[key]})
	}
	var parent []byte = []byte{}
	if previous, ok := r.refs[ref]; ok {
		parent = previous[:]
	}
	data, err := gvariant.Encode("(a{sv}aya(say)sstayay)", []any{
		items, parent, []any{}, "Export " + ref, "", swap64(uint64(time.Now().Unix())), tree[:], meta[:],
	})
	if err != nil {
		panic(err)
	}
	checksum := sha256.Sum256(data)
	r.put(checksum, "commit", data)
	r.put(checksum, "commitmeta", r.signatures(data))
	r.refs[ref] = checksum
	r.publish()
	return hex.EncodeToString(checksum[:])
}

func (r *Repo) publish() {
	names := make([]string, 0, len(r.refs))
	for name := range r.refs {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := []any{}
	for _, name := range names {
		checksum := r.refs[name]
		refs = append(refs, []any{name, []any{uint64(0), checksum[:], []any{}}})
	}
	summary, err := gvariant.Encode("(a(s(taya{sv}))a{sv})", []any{refs, []any{}})
	if err != nil {
		panic(err)
	}
	if !r.indexed {
		r.objects["summary"] = summary
		r.objects["summary.sig"] = r.signatures(summary)
		return
	}
	checksum := sha256.Sum256(summary)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	writer.Write(summary)
	writer.Close()
	r.objects["summaries/"+hex.EncodeToString(checksum[:])+".gz"] = compressed.Bytes()
	index, err := gvariant.Encode("(a{s(ayaaya{sv})}a{sv})", []any{
		[]any{[]any{"x86_64", []any{checksum[:], []any{}, []any{}}}},
		[]any{},
	})
	if err != nil {
		panic(err)
	}
	r.objects["summary.idx"] = index
	r.objects["summary.idx.sig"] = r.signatures(index)
}

// ObjectPath answers with where an object of the given kind and checksum is
// served, so a test can tamper with it through Override.
func ObjectPath(checksum, kind string) string {
	return "objects/" + checksum[:2] + "/" + checksum[2:] + "." + kind
}

// Object answers with what the repository serves at name.
func (r *Repo) Object(name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte{}, r.objects[name]...)
}
