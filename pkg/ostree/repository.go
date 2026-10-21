/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ostree

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

const (
	maxMetadataObject = 64 << 20
	maxSummary        = 256 << 20
	maxFileObject     = 16 << 30
	fetchAttempts     = 4
)

// Repository is a remote archive repository and the keys it is trusted
// through. A repository without keys cannot be read: every summary and every
// commit must carry a signature from the ring.
type Repository struct {
	URL       string
	Keyring   *Keyring
	Client    *http.Client
	UserAgent string

	// Parallel bounds the requests made at once; zero picks a default.
	Parallel int

	// Now answers with the time signatures are checked against.
	Now func() time.Time
}

func (r *Repository) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r *Repository) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Repository) resolve(name string) (string, error) {
	base, err := url.Parse(r.URL)
	if err != nil {
		return "", fmt.Errorf("ostree: invalid repository URL: %w", err)
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname())) {
		return "", fmt.Errorf("ostree: repository %s is not served over HTTPS", r.URL)
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	return base.ResolveReference(&url.URL{Path: name}).String(), nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

var errNotFound = errors.New("ostree: object not found")

// open fetches a repository file, retrying the failures a CDN produces under
// load. A missing file is reported at once, since asking again does not make
// it appear.
func (r *Repository) open(ctx context.Context, name string) (io.ReadCloser, error) {
	target, err := r.resolve(name)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := range fetchAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 250 * time.Millisecond):
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if r.UserAgent != "" {
			request.Header.Set("User-Agent", r.UserAgent)
		}
		response, err := r.client().Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			continue
		}
		switch {
		case response.StatusCode == http.StatusOK:
			return response.Body, nil
		case response.StatusCode == http.StatusNotFound:
			response.Body.Close()
			return nil, fmt.Errorf("%w: %s", errNotFound, name)
		default:
			response.Body.Close()
			last = fmt.Errorf("ostree: fetch %s: %s", name, response.Status)
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return nil, last
			}
		}
	}
	return nil, last
}

func (r *Repository) fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	body, err := r.open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("ostree: read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("ostree: %s exceeds %d bytes", name, limit)
	}
	return data, nil
}

func (r *Repository) verifySigned(ctx context.Context, data []byte, signatureFile string) error {
	if r.Keyring == nil {
		return errors.New("ostree: the repository has no trusted keys")
	}
	raw, err := r.fetch(ctx, signatureFile, maxMetadataObject)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("ostree: %s is not signed", strings.TrimSuffix(signatureFile, ".sig"))
		}
		return err
	}
	signatures, err := signaturesFrom(raw)
	if err != nil {
		return err
	}
	if err := r.Keyring.VerifyDetached(data, signatures, r.now()); err != nil {
		return fmt.Errorf("ostree: %s: %w", strings.TrimSuffix(signatureFile, ".sig"), err)
	}
	return nil
}

// signaturesFrom reads the detached metadata OSTree keeps signatures in.
func signaturesFrom(raw []byte) ([][]byte, error) {
	value, err := gvariant.Parse("a{sv}", raw)
	if err != nil {
		return nil, fmt.Errorf("ostree: decode signatures: %w", err)
	}
	dict, err := gvariant.VariantDict(value)
	if err != nil {
		return nil, err
	}
	items, ok := dict["ostree.gpgsigs"].Value.([]any)
	if !ok {
		return nil, errors.New("ostree: detached metadata carries no OpenPGP signatures")
	}
	signatures := make([][]byte, 0, len(items))
	for _, item := range items {
		if raw, ok := item.([]byte); ok {
			signatures = append(signatures, raw)
		}
	}
	return signatures, nil
}

// Ref is a ref listed in a summary.
type Ref struct {
	Name     string
	Checksum Checksum
	Metadata map[string]gvariant.Variant
}

// Summary is the signed list of refs a repository publishes.
type Summary struct {
	Refs     map[string]Ref
	Metadata map[string]gvariant.Variant
}

// FetchSummary resolves the refs the repository publishes for arch. A
// repository with an index is read through the subsummary for that
// architecture, which is a small fraction of the whole; one without falls back
// to the single summary file. Either way the file read first is signed and
// every file after it is named by a checksum it contains.
func (r *Repository) FetchSummary(ctx context.Context, arch string) (Summary, error) {
	index, err := r.fetch(ctx, "summary.idx", maxMetadataObject)
	switch {
	case err == nil:
		if err := r.verifySigned(ctx, index, "summary.idx.sig"); err != nil {
			return Summary{}, err
		}
		return r.subsummary(ctx, index, arch)
	case errors.Is(err, errNotFound):
		data, err := r.fetch(ctx, "summary", maxSummary)
		if err != nil {
			return Summary{}, err
		}
		if err := r.verifySigned(ctx, data, "summary.sig"); err != nil {
			return Summary{}, err
		}
		return parseSummary(data)
	}
	return Summary{}, err
}

func (r *Repository) subsummary(ctx context.Context, index []byte, arch string) (Summary, error) {
	value, err := gvariant.Parse(summaryIndexType, index)
	if err != nil {
		return Summary{}, fmt.Errorf("ostree: decode summary index: %w", err)
	}
	for _, item := range value.([]any)[0].([]any) {
		entry := item.([]any)
		if entry[0].(string) != arch {
			continue
		}
		checksum, err := checksumFrom(entry[1].([]any)[0])
		if err != nil {
			return Summary{}, err
		}
		compressed, err := r.fetch(ctx, "summaries/"+checksum.String()+".gz", maxSummary)
		if err != nil {
			return Summary{}, err
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return Summary{}, fmt.Errorf("ostree: open subsummary %s: %w", arch, err)
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxSummary+1))
		if err != nil {
			return Summary{}, fmt.Errorf("ostree: read subsummary %s: %w", arch, err)
		}
		if len(data) > maxSummary {
			return Summary{}, fmt.Errorf("ostree: subsummary %s is too large", arch)
		}
		if err := verifyObject(checksum, data); err != nil {
			return Summary{}, fmt.Errorf("ostree: subsummary %s: %w", arch, err)
		}
		return parseSummary(data)
	}
	return Summary{}, fmt.Errorf("ostree: the repository publishes nothing for %s", arch)
}

func parseSummary(data []byte) (Summary, error) {
	value, err := gvariant.Parse(summaryType, data)
	if err != nil {
		return Summary{}, fmt.Errorf("ostree: decode summary: %w", err)
	}
	fields := value.([]any)
	summary := Summary{Refs: map[string]Ref{}}
	for _, item := range fields[0].([]any) {
		entry := item.([]any)
		name := entry[0].(string)
		details := entry[1].([]any)
		checksum, err := checksumFrom(details[1])
		if err != nil {
			return Summary{}, err
		}
		metadata, err := gvariant.VariantDict(details[2])
		if err != nil {
			return Summary{}, err
		}
		summary.Refs[name] = Ref{Name: name, Checksum: checksum, Metadata: metadata}
	}
	if summary.Metadata, err = gvariant.VariantDict(fields[1]); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

// FetchCommit reads a commit and the signatures over it. A commit is accepted
// only when one of them was made by a trusted key, and, when the commit names
// the refs it was made for, only when ref is one of them: that binding is what
// keeps a repository from answering for one application with another one's
// signed commit.
func (r *Repository) FetchCommit(ctx context.Context, ref string, checksum Checksum) (Commit, error) {
	data, err := r.fetch(ctx, objectPath(checksum, "commit"), maxMetadataObject)
	if err != nil {
		return Commit{}, err
	}
	commit, err := parseCommit(checksum, data)
	if err != nil {
		return Commit{}, err
	}
	if err := r.verifySigned(ctx, data, objectPath(checksum, "commitmeta")); err != nil {
		return Commit{}, err
	}
	if bindings := commit.Strings("ostree.ref-binding"); len(bindings) > 0 {
		bound := false
		for _, binding := range bindings {
			bound = bound || binding == ref
		}
		if !bound {
			return Commit{}, fmt.Errorf("ostree: commit %s was not made for %s", checksum, ref)
		}
	}
	return commit, nil
}

// EntryKind is the kind of a tree entry.
type EntryKind int

const (
	EntryDir EntryKind = iota
	EntryFile
)

// Entry is one path of a commit tree. A file entry may turn out to be a
// symbolic link: archive repositories keep both as file objects and only the
// object header tells them apart.
type Entry struct {
	Path     string
	Kind     EntryKind
	Mode     uint32
	Checksum Checksum
}

const (
	maxTreeEntries = 2 << 20
	maxTreeDepth   = 256

	defaultParallelism = 32
)

// Parallelism is how many requests run at once while a tree is walked or its
// files are fetched.
func (r *Repository) parallelism() int {
	if r.Parallel > 0 {
		return r.Parallel
	}
	return defaultParallelism
}

type treeNode struct {
	entry    Entry
	files    []Entry
	children []*treeNode
}

// Tree lists every path of a commit, each directory before what it contains.
// Directories are fetched in parallel, but the listing comes out in the same
// order every time, since what a layer is recognised by is built from it.
func (r *Repository) Tree(ctx context.Context, commit Commit) ([]Entry, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	walk := treeWalk{repo: r, ctx: ctx, slots: make(chan struct{}, r.parallelism())}
	root, err := walk.visit("", commit.RootTree, commit.RootMeta, 0)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, walk.count.Load())
	var flatten func(*treeNode)
	flatten = func(node *treeNode) {
		if node.entry.Path != "" {
			entries = append(entries, node.entry)
		}
		entries = append(entries, node.files...)
		for _, child := range node.children {
			flatten(child)
		}
	}
	flatten(root)
	return entries, nil
}

type treeWalk struct {
	repo  *Repository
	ctx   context.Context
	slots chan struct{}
	count atomic.Int64
	metas sync.Map
}

// fetch holds a slot only for the request itself, never while waiting on
// children, so a deep tree cannot starve the walk of slots.
func (w *treeWalk) fetch(name string) ([]byte, error) {
	select {
	case w.slots <- struct{}{}:
	case <-w.ctx.Done():
		return nil, w.ctx.Err()
	}
	defer func() { <-w.slots }()
	return w.repo.fetch(w.ctx, name, maxMetadataObject)
}

func (w *treeWalk) meta(checksum Checksum) (dirMeta, error) {
	if cached, ok := w.metas.Load(checksum); ok {
		return cached.(dirMeta), nil
	}
	data, err := w.fetch(objectPath(checksum, "dirmeta"))
	if err != nil {
		return dirMeta{}, err
	}
	meta, err := parseDirMeta(checksum, data)
	if err != nil {
		return dirMeta{}, err
	}
	w.metas.Store(checksum, meta)
	return meta, nil
}

func (w *treeWalk) visit(prefix string, tree, metaSum Checksum, depth int) (*treeNode, error) {
	if depth > maxTreeDepth {
		return nil, errors.New("ostree: tree is too deep")
	}
	meta, err := w.meta(metaSum)
	if err != nil {
		return nil, err
	}
	data, err := w.fetch(objectPath(tree, "dirtree"))
	if err != nil {
		return nil, err
	}
	listing, err := parseDirTree(tree, data)
	if err != nil {
		return nil, err
	}
	if w.count.Add(int64(1+len(listing.files))) > maxTreeEntries {
		return nil, errors.New("ostree: tree has too many entries")
	}
	node := &treeNode{
		entry:    Entry{Path: prefix, Kind: EntryDir, Mode: meta.mode &^ modeTypeMask},
		files:    make([]Entry, 0, len(listing.files)),
		children: make([]*treeNode, len(listing.dirs)),
	}
	for _, file := range listing.files {
		node.files = append(node.files, Entry{Path: cleanJoin(prefix, file.name), Kind: EntryFile, Checksum: file.checksum})
	}
	errs := make([]error, len(listing.dirs))
	var group sync.WaitGroup
	for index, child := range listing.dirs {
		group.Add(1)
		go func() {
			defer group.Done()
			node.children[index], errs[index] = w.visit(cleanJoin(prefix, child.name), child.tree, child.meta, depth+1)
		}()
	}
	group.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return node, nil
}

// FetchFileObject downloads the compressed form of a file object into w and
// answers with how many bytes it was. Nothing is checked here: the checksum
// covers the decompressed content, so OpenFileObject is where a download is
// proven.
func (r *Repository) FetchFileObject(ctx context.Context, checksum Checksum, w io.Writer) (int64, error) {
	body, err := r.open(ctx, objectPath(checksum, "filez"))
	if err != nil {
		return 0, err
	}
	defer body.Close()
	written, err := io.Copy(w, io.LimitReader(body, maxFileObject+1))
	if err != nil {
		return written, fmt.Errorf("ostree: download %s: %w", checksum, err)
	}
	if written > maxFileObject {
		return written, fmt.Errorf("ostree: file object %s is too large", checksum)
	}
	return written, nil
}

// OpenFileObject reads a downloaded .filez object. The reader it answers with
// yields the file content and fails at its end, with nothing after, when the
// content and header do not hash to checksum, so a consumer that only commits
// what it read to EOF never commits a forged file.
func OpenFileObject(checksum Checksum, object io.ReaderAt, size int64) (FileHeader, io.Reader, error) {
	head := make([]byte, min(size, 1<<20+8))
	if _, err := object.ReadAt(head, 0); err != nil && err != io.EOF {
		return FileHeader{}, nil, err
	}
	header, offset, err := readArchiveHeader(head)
	if err != nil {
		return FileHeader{}, nil, fmt.Errorf("%w (object %s)", err, checksum)
	}
	prefix, err := header.checksumPrefix()
	if err != nil {
		return FileHeader{}, nil, err
	}
	h := sha256.New()
	h.Write(prefix)
	if header.IsSymlink() {
		if header.Size != 0 || header.Target == "" {
			return FileHeader{}, nil, fmt.Errorf("ostree: malformed symlink object %s", checksum)
		}
		if [32]byte(h.Sum(nil)) != checksum {
			return FileHeader{}, nil, fmt.Errorf("ostree: object %s does not match its checksum", checksum)
		}
		return header, bytes.NewReader(nil), nil
	}
	compressed := io.NewSectionReader(object, int64(offset), size-int64(offset))
	return header, &verifiedReader{
		source:   flate.NewReader(compressed),
		hash:     h,
		checksum: checksum,
		size:     header.Size,
	}, nil
}

type verifiedReader struct {
	source   io.ReadCloser
	hash     hash.Hash
	checksum Checksum
	size     uint64
	read     uint64
	done     bool
}

func (v *verifiedReader) Read(p []byte) (int, error) {
	if v.done {
		return 0, io.EOF
	}
	n, err := v.source.Read(p)
	v.read += uint64(n)
	v.hash.Write(p[:n])
	if v.read > v.size {
		return 0, fmt.Errorf("ostree: object %s holds more than its header declares", v.checksum)
	}
	if err == io.EOF {
		v.done = true
		v.source.Close()
		if v.read != v.size {
			return 0, fmt.Errorf("ostree: object %s holds %d bytes, its header declares %d", v.checksum, v.read, v.size)
		}
		if [32]byte(v.hash.Sum(nil)) != v.checksum {
			return 0, fmt.Errorf("ostree: object %s does not match its checksum", v.checksum)
		}
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("ostree: decompress %s: %w", v.checksum, err)
	}
	return n, nil
}
