/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/signature"
)

// A sigstore bundle is the only thing the authority reads whose bytes were
// chosen by whoever published the package. Checking one walks DER, protobuf
// and a transparency log entry, and until now the process doing that walk was
// the process holding the socket and the ledger descriptors.
//
// So it is split. The parent keeps root, the socket and the files, and never
// decodes a bundle. A child with no privileges and no network decodes it and
// answers with the two flat records a verdict is made of. A bug in the walk is
// then a bug in a process that owns nothing, and it costs one package its
// enrolment instead of costing the machine its root.
//
// The answer has to stay this dumb. The moment something richer needs to come
// back out, the parent is parsing chosen bytes again and the split has bought
// nothing.

// verifierArgument puts an authority process into the child role. It is not
// documented as a command because nobody runs it by hand: the parent passes it
// to itself.
const verifierArgument = "--verify-bundle"

// unprivilegedUID is the identity the child runs as. It is a number and never
// a name: resolving a name would put NSS between the authority and its own
// isolation, and nothing is ever stored as this user, so which uid it is
// matters only in that it is not root.
const unprivilegedUID = 65534

const (
	verifierRequestLimit  = 4 << 20
	verifierResponseLimit = 64 << 10
)

// verifierTimeout bounds how long a child may take. It is a variable so that a
// test can put a child that never answers behind it without waiting.
var verifierTimeout = 30 * time.Second

// verifierArgv is how the parent addresses its own other role. It is a
// variable only so that a test can put a process it can observe on the far
// side of the same machinery, and prove what that machinery does to it.
var verifierArgv = func() []string { return []string{"system-authority", verifierArgument} }

// ErrVerifierUnavailable means the check never ran. It is not a bundle that
// failed, and a caller must not read it as one.
var ErrVerifierUnavailable = errors.New("the signature verifier could not be started")

// verifyDirect is the real check. The child calls it, and so does an authority
// that has no privileges to separate in the first place.
var verifyDirect = signature.VerifyPublisher

type verifierRequest struct {
	Bundle []byte          `json:"bundle"`
	State  signature.State `json:"state"`
}

type verifierResponse struct {
	Verified *signature.Verified `json:"verified,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// separatedVerify checks a bundle in a child process when there is privilege
// worth separating, and in this one when there is not. An authority running as
// an ordinary user gains nothing from a fork: the child would hold everything
// the parent holds.
func separatedVerify(bundle []byte, state signature.State) (signature.Verified, error) {
	if os.Geteuid() != 0 {
		return verifyDirect(bundle, state)
	}
	answer, err := askVerifier(verifierRequest{Bundle: bundle, State: state})
	if err != nil {
		return signature.Verified{}, err
	}
	if answer.Error != "" {
		return signature.Verified{}, errors.New(answer.Error)
	}
	if answer.Verified == nil {
		return signature.Verified{}, fmt.Errorf("%w: it answered neither a verdict nor a reason", ErrVerifierUnavailable)
	}
	return *answer.Verified, nil
}

func askVerifier(request verifierRequest) (verifierResponse, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return verifierResponse{}, fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	self, err := os.Executable()
	if err != nil {
		return verifierResponse{}, fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return verifierResponse{}, fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), verifierTimeout)
	defer cancel()

	answer, err := runVerifierProcess(ctx, self, encoded, true)
	// Namespaces are a second wall and not the wall. A host that will not give
	// them out still gets the child that owns nothing, because that is the part
	// the split is for.
	if errors.Is(err, errVerifierNotIsolated) {
		answer, err = runVerifierProcess(ctx, self, encoded, false)
	}
	return answer, err
}

var errVerifierNotIsolated = errors.New("the verifier could not be isolated")

func runVerifierProcess(ctx context.Context, self string, request []byte, isolate bool) (verifierResponse, error) {
	command := exec.CommandContext(ctx, self, verifierArgv()...)
	command.Env = []string{}
	command.Stdin = bytes.NewReader(request)
	var out, fault bytes.Buffer
	command.Stdout = &out
	command.Stderr = &fault
	command.SysProcAttr = verifierAttributes(isolate)

	if err := command.Run(); err != nil {
		if isolate && startFailed(err) {
			return verifierResponse{}, errVerifierNotIsolated
		}
		if ctx.Err() != nil {
			return verifierResponse{}, fmt.Errorf("%w: it did not answer within %s", ErrVerifierUnavailable, verifierTimeout)
		}
		return verifierResponse{}, fmt.Errorf("%w: %v: %s", ErrVerifierUnavailable, err, bytes.TrimSpace(fault.Bytes()))
	}
	if out.Len() > verifierResponseLimit {
		return verifierResponse{}, fmt.Errorf("%w: it answered %d bytes", ErrVerifierUnavailable, out.Len())
	}
	decoder := json.NewDecoder(&out)
	decoder.DisallowUnknownFields()
	var answer verifierResponse
	if err := decoder.Decode(&answer); err != nil {
		return verifierResponse{}, fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	return answer, nil
}

func verifierAttributes(isolate bool) *syscall.SysProcAttr {
	attributes := &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:         unprivilegedUID,
			Gid:         unprivilegedUID,
			NoSetGroups: true,
		},
	}
	if isolate {
		// The check is offline by contract, so taking the network away enforces
		// what pkg/signature already promises instead of adding a rule.
		attributes.Cloneflags = syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC
	}
	return attributes
}

// startFailed distinguishes a child that never ran from one that ran and said
// no. Only the first is worth retrying with less isolation, because the second
// already answered.
func startFailed(err error) bool {
	var exited *exec.ExitError
	return !errors.As(err, &exited)
}

// RunVerifier is the child. It reads one request, answers one response and
// exits, and it is the only part of the authority that touches a bundle.
func RunVerifier(in io.Reader, out io.Writer) error {
	encoded, err := io.ReadAll(io.LimitReader(in, verifierRequestLimit+1))
	if err != nil {
		return fmt.Errorf("read the verification request: %w", err)
	}
	if len(encoded) > verifierRequestLimit {
		return fmt.Errorf("the verification request exceeds %d bytes", verifierRequestLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var request verifierRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("read the verification request: %w", err)
	}
	// A bundle that does not stand is an answer and not a failure of this
	// process, so it travels back as a reason rather than as an exit status
	// the parent would have to guess at.
	answer := verifierResponse{}
	verified, err := verifyDirect(request.Bundle, request.State)
	if err != nil {
		answer.Error = err.Error()
	} else {
		answer.Verified = &verified
	}
	return json.NewEncoder(out).Encode(answer)
}
