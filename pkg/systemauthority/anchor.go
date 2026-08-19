/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/signature"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	// The rules that produce a root are part of the location: a ledger written
	// under another ABI is a different ledger and must not be read as this one.
	DefaultAnchorDirectory = "/var/lib/cpak/integrity/v1"

	// Two actions, because the two answers are different. Recording what an
	// install just put on disk is the ordinary course of installing software.
	// Handing an application more than it already has is the owner of the
	// machine's call, and nothing else may make it.
	ActionEnrolAnchor  = "it.cpak.system.enrol-anchor"
	ActionWidenAnchor  = "it.cpak.system.widen-anchor"
	ActionForgetAnchor = "it.cpak.system.forget-anchor"

	// Forgetting somebody else's anchor is its own question. Removing one's own
	// is part of removing one's own software; removing another account's is the
	// owner of the machine's call, for the same reason enrolling on their
	// behalf is.
	ActionForgetAnchorOther = "it.cpak.system.forget-anchor-other"

	anchorEnrolAction  = "enrol"
	anchorForgetAction = "forget"

	// A policy travels beside the anchor its policy root was taken over, so a
	// request is the size of one anchor plus one policy.
	policySizeLimit = 32 << 10

	// A signed record carries the bundle as well. A certificate, a signature
	// and an inclusion proof are nowhere near this, and the cap is what stops
	// whoever wrote the file from deciding how much a reader reads.
	signatureBundleLimit = 256 << 10

	// anchorSizeLimit bounds the whole record: the anchor, the policy and the
	// bundle together.
	anchorSizeLimit = 512 << 10
)

var anchorRootPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrAnchorDowngrade reports an enrolment that would put an application back to
// a generation it already left. The caller has to tell it from a failure,
// because the answer is to look at what is recorded and not to try again.
var ErrAnchorDowngrade = errors.New("integrity anchor generation would go backwards")

// ErrUnsigned reports an enrolment that carries no publisher signature. It is
// an answer and not a failure: signing arrives into a world of packages nobody
// signed, and a host decides for itself whether that is a reason to refuse.
var ErrUnsigned = errors.New("the enrolment carries no publisher signature")

// ErrSignatureRequired reports an enrolment refused because this host takes
// only signed packages. The caller has to tell it from a failure too, because
// the answer is to publish a signature and not to try again.
var ErrSignatureRequired = errors.New("the host enrols only packages a publisher signed")

// ErrSignatureDowngrade reports an enrolment whose publisher counter is lower
// than the one already proven for this application. Nothing about such a
// bundle fails to verify, because an old bundle is a genuine bundle; what is
// wrong with it is that the publisher has already replaced what it names.
var ErrSignatureDowngrade = errors.New("publisher signature generation would go backwards")

// ErrSignatureLost reports an unsigned enrolment offered for an application the
// ledger already holds a publisher signature for. The record is the only place
// that fact is kept, so accepting one would turn a signed application into an
// unsigned one with nothing left to notice it by.
var ErrSignatureLost = errors.New("the enrolment drops the publisher signature already on record")

// verifyBundle is the offline check a signature is put through. It is a
// variable so that a test can drive the answer the authority acts on; nothing
// in cpak replaces it, and the default is pinned by a test that compares the
// check it ends at with signature.Verify itself.
//
// It goes through separatedVerify because the walk over a published bundle
// does not belong in the process that owns the ledger.
var verifyBundle = separatedVerify

// Enrolment is what the ledger records: the anchor, and the policy its policy
// root was taken over. The policy is kept because two hashes cannot be ordered
// against each other. Without it nobody can tell an update that narrows what an
// application may do from one that widens it, and a difference nobody can order
// has to be put to the owner every time.
type Enrolment struct {
	integrity.Anchor
	Policy *types.Override `json:"policy,omitempty"`

	// Signature is the publisher signature the installation was verified
	// against, and it is the bundle and not a verdict. A verdict would be the
	// authority's word for something a reader can prove for itself, and
	// proving it needs no network: an administrator reading this ledger on a
	// machine with no internet reaches the same answer the authority did.
	Signature *SignedState `json:"signature,omitempty"`
}

// SignedState is what a publisher signed and the bundle that proves it.
type SignedState struct {
	State  signature.State `json:"state"`
	Bundle []byte          `json:"bundle"`
}

// Signer is who signed this enrolment, checked here and now rather than read
// off the record. It answers ErrUnsigned when there is no signature at all,
// which is a different fact from a signature that does not stand and from one
// made by somebody who may not speak for this origin.
func (e Enrolment) Signer() (signature.Verified, error) {
	if e.Signature == nil {
		return signature.Verified{}, ErrUnsigned
	}
	return e.Signature.Signer(e.Origin)
}

// Signer on the state is the same question asked of a signature that is not in
// the ledger yet, which is how an installer proves one before it offers it.
// The origin is the caller's, never the payload's: a payload can name any
// origin it likes and only the certificate says who signed.
func (s SignedState) Signer(origin string) (signature.Verified, error) {
	verified, err := verifyBundle(s.Bundle, s.State)
	if err != nil {
		return signature.Verified{}, fmt.Errorf("the signature of %s does not stand: %w", origin, err)
	}
	if !verified.Identity.MatchesOrigin(origin) {
		return signature.Verified{}, fmt.Errorf("the signature of %s was made by %q", origin, verified.Identity.Repo)
	}
	return verified, nil
}

// AnchorLedger is where the enrolled applications are recorded. Every launch
// reads it and only the authority writes it, so a reader proves the file was
// produced by the owner before it believes a single field of it.
type AnchorLedger struct {
	Directory string
	OwnerUID  uint32
}

var _ integrity.AnchorWriter = AnchorLedger{}

func DefaultAnchorLedger() AnchorLedger {
	return AnchorLedger{Directory: DefaultAnchorDirectory, OwnerUID: 0}
}

func (l AnchorLedger) Load(uid uint32, origin string) (integrity.Anchor, bool, error) {
	enrolment, found, err := l.Recorded(uid, origin)
	if err != nil || !found {
		return integrity.Anchor{}, false, err
	}
	return enrolment.Anchor, true, nil
}

// Recorded reads the whole record, policy included. It is what the authority
// needs to answer how much an enrolment is asking for; a launch only ever needs
// the anchor and reads it through Load.
func (l AnchorLedger) Recorded(uid uint32, origin string) (Enrolment, bool, error) {
	path, err := l.anchorPath(uid, origin)
	if err != nil {
		return Enrolment{}, false, err
	}
	present, err := l.trustedDirectories(path)
	if err != nil || !present {
		return Enrolment{}, false, err
	}
	data, found, err := readTrusted(path, l.OwnerUID, anchorSizeLimit, "enrolled anchor")
	if err != nil || !found {
		return Enrolment{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	enrolment := Enrolment{}
	if err := decoder.Decode(&enrolment); err != nil {
		return Enrolment{}, false, fmt.Errorf("decode enrolled anchor: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Enrolment{}, false, errors.New("enrolled anchor contains multiple JSON values")
	}
	if enrolment.UID != uid || enrolment.Origin != origin {
		return Enrolment{}, false, errors.New("enrolled anchor does not match its file")
	}
	if err := validateEnrolment(enrolment); err != nil {
		return Enrolment{}, false, err
	}
	return enrolment, true, nil
}

func (l AnchorLedger) Store(anchor integrity.Anchor) error {
	return l.Record(Enrolment{Anchor: anchor})
}

// Record writes the enrolment. The policy is written next to the anchor so the
// next enrolment can be ordered against this one instead of being put to the
// owner because two hashes differ.
func (l AnchorLedger) Record(enrolment Enrolment) error {
	if err := validateEnrolment(enrolment); err != nil {
		return err
	}
	if err := l.admitSignature(enrolment); err != nil {
		return err
	}
	if err := l.admitTrust(enrolment); err != nil {
		return err
	}
	path, err := l.anchorPath(enrolment.UID, enrolment.Origin)
	if err != nil {
		return err
	}
	for _, directory := range []string{l.Directory, filepath.Dir(path)} {
		if err := ensureDirectory(directory, l.OwnerUID); err != nil {
			return err
		}
	}
	existing, found, err := l.Recorded(enrolment.UID, enrolment.Origin)
	if err != nil {
		return err
	}
	if found {
		if err := ordersAfter(existing, enrolment); err != nil {
			return err
		}
	}
	// A record that states no policy keeps the one already held for the same
	// policy root: what was proven once stays proven, and dropping it would
	// make the next narrowing look like a change nobody can order.
	if enrolment.Policy == nil && found && existing.PolicyRoot == enrolment.PolicyRoot {
		enrolment.Policy = existing.Policy
	}
	data, err := json.MarshalIndent(enrolment, "", "  ")
	if err != nil {
		return fmt.Errorf("encode anchor: %w", err)
	}
	data = append(data, '\n')
	if err := writeAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("write anchor ledger: %w", err)
	}
	return nil
}

// ordersAfter refuses an enrolment that would put an application back to
// something it already left. There are two counters and each answers for a
// different thing, so each is ordered: the anchor's says which installation
// this is, and the publisher's says which signed state it was proven against.
//
// An unordered signed state replays. The bundle of a release the publisher has
// since replaced still verifies, still names this origin and, once the anchor
// names the image it covers, still describes a real installation of it; what
// stops it being offered again is that the ledger remembers a later one.
//
// Losing the signature is refused for the same reason and not a softer one.
// Everything downstream reads the record to answer who published an
// application, and an enrolment that states nobody would be that answer.
func ordersAfter(recorded, offered Enrolment) error {
	if offered.Generation < recorded.Generation {
		return fmt.Errorf("%w: recorded %d, offered %d", ErrAnchorDowngrade, recorded.Generation, offered.Generation)
	}
	if recorded.Signature == nil {
		return nil
	}
	if offered.Signature == nil {
		return fmt.Errorf("%w: %s", ErrSignatureLost, offered.Origin)
	}
	if offered.Signature.State.Generation < recorded.Signature.State.Generation {
		return fmt.Errorf("%w: recorded %d, offered %d", ErrSignatureDowngrade,
			recorded.Signature.State.Generation, offered.Signature.State.Generation)
	}
	return nil
}

func (l AnchorLedger) Forget(uid uint32, origin string) error {
	path, err := l.anchorPath(uid, origin)
	if err != nil {
		return err
	}
	if present, err := l.trustedDirectories(path); err != nil || !present {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove enrolled anchor: %w", err)
	}
	return nil
}

// admitSignature is where a signature stops being the caller's word.
//
// A bundle that does not stand is never written down. A record naming a signer
// nobody can prove would be read as provenance by everything downstream of it,
// which is worse than a record that names none. And a host that takes only
// signed packages refuses the enrolment outright: the application stays on
// disk, unenrolled, which is the state the enforcement level already answers
// for, so the two settings compose instead of each inventing a refusal.
func (l AnchorLedger) admitSignature(enrolment Enrolment) error {
	_, err := enrolment.Signer()
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrUnsigned) {
		return err
	}
	required, err := l.signaturesRequired()
	if err != nil {
		return err
	}
	if required {
		return fmt.Errorf("%w: %s", ErrSignatureRequired, enrolment.Origin)
	}
	return nil
}

// signaturesRequired reads the host policy from beside the ledger, which is
// the only place it is ever read from. A policy that cannot be read is an
// error rather than a permission: refusing an enrolment costs a user nothing
// that a reinstall does not give back, and reading a broken file as optional
// would let whoever broke it decide the answer.
func (l AnchorLedger) signaturesRequired() (bool, error) {
	policy, err := EnforcementStore{Directory: l.Directory, OwnerUID: l.OwnerUID}.SignaturePolicy()
	if err != nil {
		return false, fmt.Errorf("read the host signature policy: %w", err)
	}
	return policy == SignaturesRequired, nil
}

// authorizationFor says which authorization an enrolment deserves. It is
// answered from what the ledger already holds against what is offered, so the
// caller cannot ask for the cheaper one: the request carries no say in this.
func (l AnchorLedger) authorizationFor(enrolment Enrolment) (string, error) {
	recorded, found, err := l.Recorded(enrolment.UID, enrolment.Origin)
	if err != nil {
		return "", err
	}
	// Nothing is being replaced, so nothing can be widened. This is trust on
	// first install: what is on disk is recorded as what the application is.
	if !found {
		return ActionEnrolAnchor, nil
	}
	// A lower generation puts the application back to something it already
	// left, and what it goes back to is not ordered against what is recorded.
	if enrolment.Generation < recorded.Generation {
		return ActionWidenAnchor, nil
	}
	// The package root may change freely as long as the policy does not: that
	// is what an update is.
	if enrolment.PolicyRoot == recorded.PolicyRoot {
		return ActionEnrolAnchor, nil
	}
	if recorded.Policy != nil && enrolment.Policy != nil && integrity.Restricts(*recorded.Policy, *enrolment.Policy) {
		return ActionEnrolAnchor, nil
	}
	// A widening is the owner of the machine's call and a publisher cannot
	// make it for them, however recently it signed. What a publisher signs is
	// the origin, the manifest and the image, and none of those is the policy
	// being enrolled: that policy is the user's own override whenever they set
	// one, so a counter moving forward over an unchanged manifest would be
	// read as consent to a widening the publisher never saw.
	return ActionWidenAnchor, nil
}

// trustedDirectories proves the ledger root along with the directory holding
// the file, before either is read from or unlinked in: an ancestor anybody may
// write is enough to replace the whole subtree, or to turn a removal here into
// a removal anywhere. A ledger that does not exist yet holds nothing, so it is
// reported as absent and not as a failure.
func (l AnchorLedger) trustedDirectories(path string) (bool, error) {
	for _, directory := range []string{l.Directory, filepath.Dir(path)} {
		if err := validateExistingDirectory(directory, l.OwnerUID); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// anchorPath keeps the origin a package coordinate and never a path: it is
// validated first, then flattened with a separator no origin part may contain,
// so two origins can never claim the same file and none of them can name a
// directory of its own.
func (l AnchorLedger) anchorPath(uid uint32, origin string) (string, error) {
	if !filepath.IsAbs(l.Directory) {
		return "", errors.New("system authority anchor path must be absolute")
	}
	if err := validateOrigin(origin); err != nil {
		return "", err
	}
	name := strings.ReplaceAll(origin, "/", ":") + ".json"
	if name != filepath.Base(name) {
		return "", errors.New("invalid package origin")
	}
	return filepath.Join(l.Directory, strconv.FormatUint(uint64(uid), 10), name), nil
}

// readTrusted returns the contents of a file the authority wrote. Anything that
// is not a plain file of the expected owner, or that somebody else may write,
// is refused instead of read: what came out of it would be that writer's value
// and not the authority's.
func readTrusted(path string, owner uint32, limit int, subject string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", subject, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, false, fmt.Errorf("%s is not trusted", subject)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner {
		return nil, false, fmt.Errorf("%s has an unexpected owner", subject)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", subject, err)
	}
	if len(data) > limit {
		return nil, false, fmt.Errorf("%s is too large", subject)
	}
	return data, true, nil
}

func validateAnchor(anchor integrity.Anchor) error {
	if anchor.ABI != integrity.ABIVersion {
		return errors.New("integrity anchor was written for another abi")
	}
	if err := validateOrigin(anchor.Origin); err != nil {
		return err
	}
	if err := anchor.ValidateDigests(); err != nil {
		return err
	}
	for _, root := range []string{anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot} {
		if !anchorRootPattern.MatchString(root) {
			return errors.New("invalid integrity anchor root")
		}
	}
	// A launch root that does not follow from the two halves it is made of
	// would let a launch be recognised by a value nobody can recompute.
	if anchor.LaunchRoot != integrity.LaunchRoot(anchor.PackageRoot, anchor.PolicyRoot) {
		return errors.New("integrity anchor launch root does not follow from its package and policy roots")
	}
	return nil
}

// validateEnrolment is where the policy stops being the caller's word. It is
// believed for one reason only, that it hashes to the policy root being
// enrolled: a caller that sent a narrow policy with a wide root would be asking
// to be authorized for something other than what it is recording.
func validateEnrolment(enrolment Enrolment) error {
	if err := validateAnchor(enrolment.Anchor); err != nil {
		return err
	}
	if err := validateSignedState(enrolment); err != nil {
		return err
	}
	if enrolment.Policy == nil {
		return nil
	}
	root, err := integrity.PolicyRoot(*enrolment.Policy)
	if err != nil {
		return fmt.Errorf("derive the policy root of the enrolment: %w", err)
	}
	if root != enrolment.PolicyRoot {
		return errors.New("enrolment policy does not hash to its policy root")
	}
	return nil
}

// validateSignedState is the shape of a signature, not its worth. It runs on
// every read as well as every write, so it must never depend on cryptography:
// a ledger whose records became unreadable the day a trust root was refreshed
// would take every launch on the host with it. Whether a signature stands is
// asked by Signer, where the answer is reported instead of being fatal.
func validateSignedState(enrolment Enrolment) error {
	if enrolment.Signature == nil {
		return nil
	}
	if err := enrolment.Signature.State.Validate(); err != nil {
		return fmt.Errorf("enrolment signed state: %w", err)
	}
	// The state names the package it is about, so a bundle covering somebody
	// else's package can never be filed under this application.
	if enrolment.Signature.State.Origin != enrolment.Origin {
		return errors.New("enrolment signed state names another package")
	}
	if err := bindsTheAnchor(enrolment); err != nil {
		return err
	}
	bundle := enrolment.Signature.Bundle
	if len(bundle) == 0 || len(bundle) > signatureBundleLimit {
		return errors.New("enrolment signature bundle is not a bundle")
	}
	// A bundle is a JSON document and travels the bus as text, so anything
	// that is not text would fail there instead of here, where it can be named.
	if !utf8.Valid(bundle) {
		return errors.New("enrolment signature bundle is not text")
	}
	return nil
}

// bindsTheAnchor is what makes a signature a binding and not a label. The
// anchor says what this launch is made of and the state says what the
// publisher shipped, and unless the two name the same image and the same
// manifest the only thing they agree on is an origin, which every other
// release of that origin agrees on as well.
//
// A signed enrolment that leaves either of them out is refused rather than
// recorded unbound. This runs on every read too, and it costs no record its
// anchor: nothing outside a test has ever recorded a signature at all, so
// there is none on any host that lacks them.
func bindsTheAnchor(enrolment Enrolment) error {
	if enrolment.ImageDigest == "" || enrolment.ManifestDigest == "" {
		return errors.New("a signed enrolment must state the image and the manifest its anchor describes")
	}
	state := enrolment.Signature.State
	if state.ImageDigest != enrolment.ImageDigest {
		return fmt.Errorf("enrolment signed state names image %s and its anchor names %s", state.ImageDigest, enrolment.ImageDigest)
	}
	if state.ManifestSHA256 != enrolment.ManifestDigest {
		return fmt.Errorf("enrolment signed state names manifest %s and its anchor names %s", state.ManifestSHA256, enrolment.ManifestDigest)
	}
	return nil
}

// EnrolAnchor records what a launch of an application is allowed to be. It
// states no policy, so the next enrolment that changes the policy root is put
// to the owner: an installer that holds the effective override should call
// EnrolAnchorWithPolicy instead.
func EnrolAnchor(anchor integrity.Anchor) error {
	return EnrolAnchorWithPolicy(anchor, nil)
}

// EnrolAnchorWithPolicy records the anchor together with the policy its policy
// root was taken over. Sending it is what lets the authority tell an update
// that narrows what an application may do from one that widens it, without
// asking the owner about every install.
func EnrolAnchorWithPolicy(anchor integrity.Anchor, policy *types.Override) error {
	return EnrolAnchorWithSignature(anchor, policy, nil)
}

// EnrolAnchorWithSignature records the anchor, the policy its policy root was
// taken over, and the publisher signature the installation was verified
// against.
//
// The bundle travels as evidence and never as a verdict: the authority checks
// it itself, which is what lets it tell an update the publisher stated from
// one nobody did without believing a word of what the installer says.
func EnrolAnchorWithSignature(anchor integrity.Anchor, policy *types.Override, signed *SignedState) error {
	enrolment := Enrolment{Anchor: anchor, Policy: policy, Signature: signed}
	if err := validateEnrolment(enrolment); err != nil {
		return err
	}
	if signed == nil {
		// The anchor names the application it is about, so the request carries
		// no second copy of the origin that could disagree with it.
		return dispatchIntegrity(socketRequest{Action: anchorEnrolAction, Anchor: &anchor, Policy: policy})
	}
	return dispatchSignedEnrolment(enrolment)
}

// dispatchSignedEnrolment walks the transports a signature can travel. There
// are two: root writes the ledger it owns, and everybody else goes through the
// bus, which is what carries an interactive authorization.
//
// It does not fall back to the socket. That request carries no signature, and
// sending the enrolment over it without one would record a signed installation
// as unsigned, which is the one downgrade this whole design exists to make
// impossible. A host with no system bus is a host where this is recorded by
// root, and the caller is told so.
func dispatchSignedEnrolment(enrolment Enrolment) error {
	if os.Geteuid() == 0 {
		return asRefusal(DefaultAnchorLedger().Record(enrolment))
	}
	err := signedEnrolmentOverBus(enrolment)
	if errors.Is(err, errTransportUnavailable) {
		return ErrNoAuthority
	}
	return asRefusal(err)
}

func signedEnrolmentOverBus(enrolment Enrolment) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	policy, err := encodePolicy(enrolment.Policy)
	if err != nil {
		return err
	}
	state, err := json.Marshal(enrolment.Signature.State)
	if err != nil {
		return fmt.Errorf("encode the signed state: %w", err)
	}
	anchor := enrolment.Anchor
	call := connection.Object(BusName, ObjectPath).Call(InterfaceName+".EnrolSignedAnchor", 0,
		int32(anchor.ABI), anchor.UID, anchor.Origin, anchor.Generation,
		anchor.ImageDigest, anchor.ManifestDigest,
		anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, policy,
		string(state), string(enrolment.Signature.Bundle))
	if call.Err == nil {
		return nil
	}
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	return fmt.Errorf("%s: %w", integritySubject(anchorEnrolAction), call.Err)
}

// EnrolSignedAnchor is EnrolAnchor with the publisher signature beside it. It
// is a second method and not more arguments on the first, because what a bus
// method takes is part of what its callers already speak.
func (s *Service) EnrolSignedAnchor(sender dbus.Sender, abi int32, uid uint32, origin string, generation uint64, imageDigest, manifestDigest, packageRoot, policyRoot, launchRoot, policy, state, bundle string) *dbus.Error {
	if stale := refuseIfStale(); stale != nil {
		return stale
	}
	decoded, err := decodePolicy(policy)
	if err != nil {
		return invalidRequest(err)
	}
	signed, err := decodeSignedState(state, bundle)
	if err != nil {
		return invalidRequest(err)
	}
	return s.enrolThrough(sender, Enrolment{
		Anchor: integrity.Anchor{
			ABI:            int(abi),
			UID:            uid,
			Origin:         origin,
			Generation:     generation,
			ImageDigest:    imageDigest,
			ManifestDigest: manifestDigest,
			PackageRoot:    packageRoot,
			PolicyRoot:     policyRoot,
			LaunchRoot:     launchRoot,
		},
		Policy:    decoded,
		Signature: signed,
	})
}

// enrolThrough is the enrolment flow once the wire values are back together:
// prove the request, decide how hard to ask, ask, record.
func (s *Service) enrolThrough(sender dbus.Sender, enrolment Enrolment) *dbus.Error {
	if err := validateEnrolment(enrolment); err != nil {
		return invalidRequest(err)
	}
	// A bundle that does not stand is refused before anybody is asked for a
	// password, because the enrolment cannot be recorded whatever the answer
	// would have been.
	if _, err := enrolment.Signer(); err != nil && !errors.Is(err, ErrUnsigned) {
		return invalidRequest(err)
	}
	if s.Authorizer == nil {
		return denied(errors.New("authorization service is unavailable"))
	}
	action, err := s.enrolmentAction(sender, enrolment)
	if err != nil {
		return failed(err)
	}
	if err := s.Authorizer.Authorize(sender, action, map[string]string{
		"package-origin": enrolment.Origin,
		"target-uid":     strconv.FormatUint(uint64(enrolment.UID), 10),
		"generation":     strconv.FormatUint(enrolment.Generation, 10),
	}); err != nil {
		return denied(err)
	}
	if err := s.Anchors.Record(enrolment); err != nil {
		return failed(err)
	}
	return nil
}

// decodeSignedState puts the signature back together from the two plain values
// the bus carries it as.
func decodeSignedState(state, bundle string) (*SignedState, error) {
	if state == "" && bundle == "" {
		return nil, nil
	}
	if len(state) > signatureBundleLimit || len(bundle) > signatureBundleLimit {
		return nil, errors.New("signed state is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(state))
	decoder.DisallowUnknownFields()
	decoded := signature.State{}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode the signed state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("signed state contains multiple JSON values")
	}
	return &SignedState{State: decoded, Bundle: []byte(bundle)}, nil
}

func ForgetAnchor(uid uint32, origin string) error {
	if err := validateOrigin(origin); err != nil {
		return err
	}
	return dispatchIntegrity(socketRequest{Action: anchorForgetAction, Origin: origin, UID: uid})
}

// LoadAnchor answers what the ledger holds for an application. It reads the
// ledger where it lies, on every account and without an authority, because the
// ledger is deliberately readable by everyone: a launch has to be able to check
// itself on a host where nothing is running, and reading takes no privilege.
// Only changing what is recorded does, and that is what walks the transports.
func LoadAnchor(uid uint32, origin string) (integrity.Anchor, bool, error) {
	if err := validateOrigin(origin); err != nil {
		return integrity.Anchor{}, false, err
	}
	return DefaultAnchorLedger().Load(uid, origin)
}

// RecordedAnchor answers with the whole record, signature and all, and reads
// it where it lies for the same reason LoadAnchor does: reading takes no
// privilege, and what the ledger holds about who published an application has
// to be readable on a host where nothing is running.
func RecordedAnchor(uid uint32, origin string) (Enrolment, bool, error) {
	if err := validateOrigin(origin); err != nil {
		return Enrolment{}, false, err
	}
	return DefaultAnchorLedger().Recorded(uid, origin)
}

// dispatchIntegrity walks the transports the way the session client does and
// for the same reason: root already holds the privilege the authority exists to
// lend, and a transport that answered is final.
func dispatchIntegrity(message socketRequest) error {
	if os.Geteuid() == 0 {
		if message.Action == enforcementSetAction {
			return applyEnforcement(DefaultEnforcementStore(), message)
		}
		return applyAnchor(DefaultAnchorLedger(), message)
	}
	if err := retryPastStale(func() error { return integrityOverBus(message) }); !errors.Is(err, errTransportUnavailable) {
		return asRefusal(err)
	}
	if err := requestOverSocket(DefaultSocketPath, message); !errors.Is(err, errTransportUnavailable) {
		return asRefusal(err)
	}
	return ErrNoAuthority
}

func integrityOverBus(message socketRequest) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	object := connection.Object(BusName, ObjectPath)
	call, err := integrityCall(object, message)
	if err != nil {
		return err
	}
	if call.Err == nil {
		return nil
	}
	// A bus that cannot produce the authority is a transport that failed, not a
	// refusal, so the caller is free to try the socket.
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	return fmt.Errorf("%s: %w", integritySubject(message.Action), call.Err)
}

func integrityCall(object dbus.BusObject, message socketRequest) (*dbus.Call, error) {
	switch {
	case message.Action == anchorEnrolAction && message.Anchor != nil:
		policy, err := encodePolicy(message.Policy)
		if err != nil {
			return nil, err
		}
		anchor := message.Anchor
		return object.Call(InterfaceName+".EnrolAnchor", 0, int32(anchor.ABI), anchor.UID, anchor.Origin,
			anchor.Generation, anchor.ImageDigest, anchor.ManifestDigest,
			anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, policy), nil
	case message.Action == anchorForgetAction:
		return object.Call(InterfaceName+".ForgetAnchor", 0, message.UID, message.Origin), nil
	case message.Action == enforcementSetAction:
		return object.Call(InterfaceName+".SetEnforcement", 0, message.Level), nil
	}
	return nil, errors.New("unsupported system authority action")
}

func integritySubject(action string) string {
	switch action {
	case anchorEnrolAction:
		return "enrol integrity anchor"
	case anchorForgetAction:
		return "forget integrity anchor"
	}
	return "set the integrity enforcement level"
}

// The bus carries plain values, so the policy travels as the JSON it is written
// as everywhere else. Nothing rests on the encoding: the authority believes the
// policy only because it hashes to the root being enrolled.
func encodePolicy(policy *types.Override) (string, error) {
	if policy == nil {
		return "", nil
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("encode enrolment policy: %w", err)
	}
	return string(encoded), nil
}

func decodePolicy(encoded string) (*types.Override, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > policySizeLimit {
		return nil, errors.New("enrolment policy is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	policy := types.Override{}
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode enrolment policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("enrolment policy contains multiple JSON values")
	}
	return &policy, nil
}

// applyAnchor answers the requests that carry an anchor, whichever transport
// brought them, so the ledger is entered through the same validation every time.
func applyAnchor(ledger AnchorLedger, message socketRequest) error {
	switch message.Action {
	case anchorEnrolAction:
		if message.Anchor == nil {
			return errors.New("invalid integrity anchor")
		}
		enrolment := Enrolment{Anchor: *message.Anchor, Policy: message.Policy}
		if err := validateEnrolment(enrolment); err != nil {
			return err
		}
		return ledger.Record(enrolment)
	case anchorForgetAction:
		if err := validateOrigin(message.Origin); err != nil {
			return err
		}
		return ledger.Forget(message.UID, message.Origin)
	default:
		return errors.New("unsupported system authority action")
	}
}

// asRefusal recognises the refusals a caller has to act on differently after
// they crossed a transport, where an error is only its own text. Each of them
// says the enrolment will not be recorded however often it is offered, which
// is the one thing a retry cannot fix.
func asRefusal(err error) error {
	if err == nil {
		return nil
	}
	for _, refusal := range []error{ErrAnchorDowngrade, ErrSignatureDowngrade, ErrSignatureLost, ErrTrustRefused} {
		if errors.Is(err, refusal) {
			return err
		}
		if strings.Contains(err.Error(), refusal.Error()) {
			return remoteRefusal{message: err.Error(), refusal: refusal}
		}
	}
	return err
}

type remoteRefusal struct {
	message string
	refusal error
}

func (e remoteRefusal) Error() string { return e.message }

func (e remoteRefusal) Unwrap() error { return e.refusal }
