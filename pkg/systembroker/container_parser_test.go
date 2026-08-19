/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
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
	if _, err := validateContainerMount(ContainerMount{Source: readOnly, Target: "/data"}, grants); err == nil {
		t.Fatal("read-only filesystem grant was promoted to read-write")
	}
	if _, err := validateContainerMount(ContainerMount{Source: readOnly, Target: "/data", ReadOnly: true}, grants); err != nil {
		t.Fatalf("read-only mount was rejected: %v", err)
	}
	if _, err := validateContainerMount(ContainerMount{Source: readWrite, Target: "/data"}, grants); err != nil {
		t.Fatalf("read-write mount was rejected: %v", err)
	}
	if _, err := validateContainerMount(ContainerMount{Source: outside, Target: "/data", ReadOnly: true}, grants); err == nil {
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

func TestContainerMountIsIssuedAgainstTheResolvedSource(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	granted := filepath.Join(root, "granted")
	target := filepath.Join(granted, "target")
	outside := filepath.Join(root, "outside")
	for _, path := range []string{target, outside} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(granted, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}

	grants := []ContainerPathGrant{{Path: granted}}
	request := ContainerRequest{Operation: "run", Image: "example.invalid/demo:1", Mounts: []ContainerMount{{Source: alias, Target: "/data"}}}
	arguments, err := containerCreateArguments("app-id", grants, request)
	if err != nil {
		t.Fatal(err)
	}

	// The caller owns the source it was checked on and rewrites it, which it
	// is free to do at any moment between the check and the bind.
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}

	source, _, found := strings.Cut(containerVolumeArgument(t, arguments), ":")
	if !found {
		t.Fatal("the create arguments carry no volume")
	}
	bound, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if !pathContains(granted, bound) {
		t.Fatalf("container mount reaches %q, outside the granted paths", bound)
	}
	if source != target {
		t.Fatalf("container mount source: got %q, want %q", source, target)
	}
}

func TestContainerMountRefusesAColonInEitherHalf(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	granted := filepath.Join(root, "granted")
	// A colon is a field separator in the volume argument, and a directory
	// named with one is a directory the caller may create inside its grant.
	source := filepath.Join(granted, "a:z")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(granted, "plain")
	if err := os.MkdirAll(plain, 0755); err != nil {
		t.Fatal(err)
	}

	grants := []ContainerPathGrant{{Path: granted}}
	if _, err := validateContainerMount(ContainerMount{Source: source, Target: "/data"}, grants); err == nil {
		t.Fatal("a mount source carrying a colon was accepted")
	}
	if _, err := validateContainerMount(ContainerMount{Source: plain, Target: "/data:z"}, grants); err == nil {
		t.Fatal("a mount target carrying a podman option was accepted")
	}
	if _, err := validateContainerMount(ContainerMount{Source: plain, Target: "/data"}, grants); err != nil {
		t.Fatalf("an ordinary mount was rejected: %v", err)
	}
}

func TestContainerNameSpelledLikeAnIdentifierIsRefused(t *testing.T) {
	identifier := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	for _, name := range []string{identifier, identifier[:12]} {
		request := ContainerRequest{Operation: "run", Image: "example.invalid/demo:1", Name: name}
		if _, _, err := containerArguments("app-id", map[string]bool{types.HostActionContainersManageOwned: true}, nil, request); err == nil {
			t.Fatalf("a container named %q can stand in front of the container it names", name)
		}
	}
	request := ContainerRequest{Operation: "run", Image: "example.invalid/demo:1", Name: "demo-cafe"}
	if _, _, err := containerArguments("app-id", map[string]bool{types.HostActionContainersManageOwned: true}, nil, request); err != nil {
		t.Fatalf("an ordinary container name was refused: %v", err)
	}
}

func TestOwnedContainerRequestTargetsTheResolvedIdentifier(t *testing.T) {
	identifier := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	directory := t.TempDir()
	recorded := filepath.Join(directory, "inspected")
	binary := filepath.Join(directory, "podman")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + recorded + "\nprintf '%s app-id\\n' " + identifier + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	resolved, err := ownedContainerRequest(context.Background(), binary, "app-id", ContainerRequest{Operation: "stop", Resources: []string{"demo"}})
	if err != nil {
		t.Fatal(err)
	}
	manage := map[string]bool{types.HostActionContainersManageOwned: true}
	arguments, owned, err := containerArguments("app-id", manage, nil, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !owned || !reflect.DeepEqual(arguments, []string{"stop", identifier}) {
		t.Fatalf("owned container arguments: %v, owned=%t", arguments, owned)
	}
	inspected, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(inspected), " demo") {
		t.Fatalf("ownership was not checked on the requested name: %q", inspected)
	}
}

func TestOwnedContainerRequestRefusesAForeignContainer(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "podman")
	script := "#!/bin/sh\nprintf '%s other\\n' e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ownedContainerRequest(context.Background(), binary, "app-id", ContainerRequest{Operation: "stop", Resources: []string{"demo"}}); err == nil {
		t.Fatal("a container owned by another cpak was accepted")
	}
}

func containerVolumeArgument(t *testing.T, arguments []string) string {
	t.Helper()
	for index, argument := range arguments {
		if argument == "--volume" && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	t.Fatalf("the create arguments carry no volume: %v", arguments)
	return ""
}
