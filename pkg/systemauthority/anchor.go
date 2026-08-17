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

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/integrity"
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

	anchorEnrolAction  = "enrol"
	anchorForgetAction = "forget"

	// A record carries the policy its policy root was taken over, so the cap is
	// the anchor plus one policy and not the anchor alone.
	anchorSizeLimit = 32 << 10
)

var anchorRootPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrAnchorDowngrade reports an enrolment that would put an application back to
// a generation it already left. The caller has to tell it from a failure,
// because the answer is to look at what is recorded and not to try again.
var ErrAnchorDowngrade = errors.New("integrity anchor generation would go backwards")

// Enrolment is what the ledger records: the anchor, and the policy its policy
// root was taken over. The policy is kept because two hashes cannot be ordered
// against each other. Without it nobody can tell an update that narrows what an
// application may do from one that widens it, and a difference nobody can order
// has to be put to the owner every time.
type Enrolment struct {
	integrity.Anchor
	Policy *types.Override `json:"policy,omitempty"`
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
	if found && enrolment.Generation < existing.Generation {
		return fmt.Errorf("%w: recorded %d, offered %d", ErrAnchorDowngrade, existing.Generation, enrolment.Generation)
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
	if recorded.Policy == nil || enrolment.Policy == nil {
		return ActionWidenAnchor, nil
	}
	if integrity.Restricts(*recorded.Policy, *enrolment.Policy) {
		return ActionEnrolAnchor, nil
	}
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
	if err := validateEnrolment(Enrolment{Anchor: anchor, Policy: policy}); err != nil {
		return err
	}
	// The anchor names the application it is about, so the request carries no
	// second copy of the origin that could disagree with it.
	return dispatchIntegrity(socketRequest{Action: anchorEnrolAction, Anchor: &anchor, Policy: policy})
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
	if err := integrityOverBus(message); !errors.Is(err, errTransportUnavailable) {
		return asDowngrade(err)
	}
	if err := requestOverSocket(DefaultSocketPath, message); !errors.Is(err, errTransportUnavailable) {
		return asDowngrade(err)
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
			anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot, policy), nil
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
	if len(encoded) > anchorSizeLimit {
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

// asDowngrade recognises the one refusal a caller has to act on differently
// after it crossed a transport, where an error is only its own text.
func asDowngrade(err error) error {
	if err == nil || errors.Is(err, ErrAnchorDowngrade) {
		return err
	}
	if strings.Contains(err.Error(), ErrAnchorDowngrade.Error()) {
		return remoteDowngrade{message: err.Error()}
	}
	return err
}

type remoteDowngrade struct {
	message string
}

func (e remoteDowngrade) Error() string { return e.message }

func (e remoteDowngrade) Unwrap() error { return ErrAnchorDowngrade }
