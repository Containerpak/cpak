/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestParseContainerCommands(t *testing.T) {
	tail := 10
	tests := []struct {
		args []string
		want ContainerRequest
	}{
		{
			args: []string{"ps", "--format", "json"},
			want: ContainerRequest{Operation: "ps", Format: "json"},
		},
		{
			args: []string{"logs", "--follow", "--tail", "10", "demo"},
			want: ContainerRequest{Operation: "logs", Resources: []string{"demo"}, Follow: true, Tail: &tail},
		},
		{
			args: []string{"run", "--name", "demo", "--env", "MODE=test", "registry.example/demo:1", "serve"},
			want: ContainerRequest{Operation: "run", Name: "demo", Environment: []string{"MODE=test"}, Image: "registry.example/demo:1", Command: []string{"serve"}},
		},
	}
	for _, test := range tests {
		got, err := parseContainerCommand(test.args)
		if err != nil {
			t.Fatalf("parse %v: %v", test.args, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parse %v: got %+v, want %+v", test.args, got, test.want)
		}
	}
}

func TestParseContainerCommandRejectsPrivilegedSurfaces(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--privileged", "example.invalid/demo:1"},
		{"run", "--device", "/dev/kvm", "example.invalid/demo:1"},
		{"run", "--network", "host", "example.invalid/demo:1"},
		{"exec", "-it", "demo", "sh"},
	} {
		if _, err := parseContainerCommand(args); err == nil {
			t.Fatalf("privileged container arguments were accepted: %v", args)
		}
	}
}

func TestContainerArgumentsEnforceCapabilitiesAndOwnership(t *testing.T) {
	read := map[string]bool{types.HostActionContainersRead: true}
	if _, _, err := containerArguments("app-id", nil, nil, ContainerRequest{Operation: "ps"}); err == nil {
		t.Fatal("container read was accepted without a capability")
	}
	arguments, owned, err := containerArguments("app-id", read, nil, ContainerRequest{Operation: "ps", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if owned || !reflect.DeepEqual(arguments, []string{"ps", "--all"}) {
		t.Fatalf("container read arguments: %v, owned=%t", arguments, owned)
	}

	manage := map[string]bool{types.HostActionContainersManageOwned: true}
	arguments, owned, err = containerArguments("app-id", manage, nil, ContainerRequest{Operation: "stop", Resources: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !owned || !reflect.DeepEqual(arguments, []string{"stop", "demo"}) {
		t.Fatalf("container mutation arguments: %v, owned=%t", arguments, owned)
	}

	arguments, owned, err = containerArguments("app-id", manage, nil, ContainerRequest{Operation: "run", Image: "example.invalid/demo:1"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	if owned || !strings.Contains(joined, "--label "+containerOwnerLabel+"=app-id") || strings.Contains(joined, "--privileged") {
		t.Fatalf("container creation arguments: %v, owned=%t", arguments, owned)
	}
}

func TestContainerMountsStayWithinFilesystemGrant(t *testing.T) {
	root := t.TempDir()
	readOnly := filepath.Join(root, "read-only")
	readWrite := filepath.Join(root, "read-write")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{readOnly, readWrite, outside} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	grants := []ContainerPathGrant{{Path: readOnly, ReadOnly: true}, {Path: readWrite}}
	if err := validateContainerMount(ContainerMount{Source: readOnly, Target: "/data"}, grants); err == nil {
		t.Fatal("read-only filesystem grant was promoted to read-write")
	}
	if err := validateContainerMount(ContainerMount{Source: readOnly, Target: "/data", ReadOnly: true}, grants); err != nil {
		t.Fatalf("read-only mount was rejected: %v", err)
	}
	if err := validateContainerMount(ContainerMount{Source: readWrite, Target: "/data"}, grants); err != nil {
		t.Fatalf("read-write mount was rejected: %v", err)
	}
	if err := validateContainerMount(ContainerMount{Source: outside, Target: "/data", ReadOnly: true}, grants); err == nil {
		t.Fatal("mount outside the filesystem grants was accepted")
	}
}

func TestValidateContainerShapeRejectsIrrelevantFields(t *testing.T) {
	for _, request := range []ContainerRequest{
		{Operation: "ps", Command: []string{"id"}},
		{Operation: "inspect", Resources: []string{"demo"}, Environment: []string{"A=B"}},
		{Operation: "start", Resources: []string{"demo"}, Image: "example.invalid/demo:1"},
	} {
		if err := validateContainerRequest(request); err == nil {
			t.Fatalf("invalid typed request was accepted: %+v", request)
		}
	}
}

func TestContainerBackendSelection(t *testing.T) {
	if got := containerBackend(ContainerRequest{}); got != "podman" {
		t.Fatalf("default backend: got %q, want podman", got)
	}
	if got := containerBackend(ContainerRequest{Backend: "docker"}); got != "docker" {
		t.Fatalf("selected backend: got %q, want docker", got)
	}
	if err := validateContainerRequest(ContainerRequest{Backend: "nerdctl", Operation: "ps"}); err == nil {
		t.Fatal("unsupported container backend was accepted")
	}
}

func TestTrustedContainerBinaryReportsMissingSelectedBackend(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := trustedContainerBinary("docker"); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("missing Docker backend error: %v", err)
	}
}
