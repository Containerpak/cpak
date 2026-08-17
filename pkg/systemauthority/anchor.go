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
)

const (
	// The rules that produce a root are part of the location: a ledger written
	// under another ABI is a different ledger and must not be read as this one.
	DefaultAnchorDirectory = "/var/lib/cpak/integrity/v1"

	ActionEnrollAnchor = "it.cpak.system.enroll-anchor"
	ActionForgetAnchor = "it.cpak.system.forget-anchor"

	anchorEnrollAction = "enroll"
	anchorForgetAction = "forget"
	anchorSizeLimit    = 4096
)

var anchorRootPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrAnchorDowngrade reports an enrolment that would put an application back to
// a generation it already left. The caller has to tell it from a failure,
// because the answer is to look at what is recorded and not to try again.
var ErrAnchorDowngrade = errors.New("integrity anchor generation would go backwards")

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
	path, err := l.anchorPath(uid, origin)
	if err != nil {
		return integrity.Anchor{}, false, err
	}
	present, err := l.trustedDirectories(path)
	if err != nil || !present {
		return integrity.Anchor{}, false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return integrity.Anchor{}, false, nil
	}
	if err != nil {
		return integrity.Anchor{}, false, fmt.Errorf("read enrolled anchor: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return integrity.Anchor{}, false, errors.New("enrolled anchor is not trusted")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != l.OwnerUID {
		return integrity.Anchor{}, false, errors.New("enrolled anchor has an unexpected owner")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return integrity.Anchor{}, false, fmt.Errorf("read enrolled anchor: %w", err)
	}
	if len(data) > anchorSizeLimit {
		return integrity.Anchor{}, false, errors.New("enrolled anchor is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	anchor := integrity.Anchor{}
	if err := decoder.Decode(&anchor); err != nil {
		return integrity.Anchor{}, false, fmt.Errorf("decode enrolled anchor: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return integrity.Anchor{}, false, errors.New("enrolled anchor contains multiple JSON values")
	}
	if anchor.UID != uid || anchor.Origin != origin {
		return integrity.Anchor{}, false, errors.New("enrolled anchor does not match its file")
	}
	if err := validateAnchor(anchor); err != nil {
		return integrity.Anchor{}, false, err
	}
	return anchor, true, nil
}

func (l AnchorLedger) Store(anchor integrity.Anchor) error {
	if err := validateAnchor(anchor); err != nil {
		return err
	}
	path, err := l.anchorPath(anchor.UID, anchor.Origin)
	if err != nil {
		return err
	}
	for _, directory := range []string{l.Directory, filepath.Dir(path)} {
		if err := ensureDirectory(directory, l.OwnerUID); err != nil {
			return err
		}
	}
	existing, found, err := l.Load(anchor.UID, anchor.Origin)
	if err != nil {
		return err
	}
	if found && anchor.Generation < existing.Generation {
		return fmt.Errorf("%w: recorded %d, offered %d", ErrAnchorDowngrade, existing.Generation, anchor.Generation)
	}
	data, err := json.MarshalIndent(anchor, "", "  ")
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

// EnrollAnchor records what a launch of an application is allowed to be.
func EnrollAnchor(anchor integrity.Anchor) error {
	if err := validateAnchor(anchor); err != nil {
		return err
	}
	// The anchor names the application it is about, so the request carries no
	// second copy of the origin that could disagree with it.
	return dispatchAnchor(socketRequest{Action: anchorEnrollAction, Anchor: &anchor})
}

func ForgetAnchor(uid uint32, origin string) error {
	if err := validateOrigin(origin); err != nil {
		return err
	}
	return dispatchAnchor(socketRequest{Action: anchorForgetAction, Origin: origin, UID: uid})
}

// dispatchAnchor walks the transports the way the session client does and for
// the same reason: root already holds the privilege the authority exists to
// lend, and a transport that answered is final.
func dispatchAnchor(message socketRequest) error {
	if os.Geteuid() == 0 {
		return applyAnchor(DefaultAnchorLedger(), message)
	}
	if err := anchorOverBus(message); !errors.Is(err, errTransportUnavailable) {
		return asDowngrade(err)
	}
	if err := requestOverSocket(DefaultSocketPath, message); !errors.Is(err, errTransportUnavailable) {
		return asDowngrade(err)
	}
	return ErrNoAuthority
}

func anchorOverBus(message socketRequest) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	object := connection.Object(BusName, ObjectPath)
	var call *dbus.Call
	switch {
	case message.Action == anchorEnrollAction && message.Anchor != nil:
		anchor := message.Anchor
		call = object.Call(InterfaceName+".EnrollAnchor", 0, int32(anchor.ABI), anchor.UID, anchor.Origin,
			anchor.Generation, anchor.PackageRoot, anchor.PolicyRoot, anchor.LaunchRoot)
	case message.Action == anchorForgetAction:
		call = object.Call(InterfaceName+".ForgetAnchor", 0, message.UID, message.Origin)
	default:
		return errors.New("unsupported system authority action")
	}
	if call.Err == nil {
		return nil
	}
	// A bus that cannot produce the authority is a transport that failed, not a
	// refusal, so the caller is free to try the socket.
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	if message.Action == anchorEnrollAction {
		return fmt.Errorf("enroll integrity anchor: %w", call.Err)
	}
	return fmt.Errorf("forget integrity anchor: %w", call.Err)
}

// applyAnchor answers the requests that carry an anchor, whichever transport
// brought them, so the ledger is entered through the same validation every time.
func applyAnchor(ledger AnchorLedger, message socketRequest) error {
	switch message.Action {
	case anchorEnrollAction:
		if message.Anchor == nil {
			return errors.New("invalid integrity anchor")
		}
		if err := validateAnchor(*message.Anchor); err != nil {
			return err
		}
		return ledger.Store(*message.Anchor)
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
