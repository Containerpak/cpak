package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestResolveManifestImageTracksGitSource(t *testing.T) {
	manifest := &types.CpakManifest{
		Image:    "ghcr.io/bottlesdevs/bottles:main",
		ImageRef: "source",
	}
	tests := []struct {
		name    string
		branch  string
		release string
		commit  string
		want    string
	}{
		{name: "branch", branch: "feature/cpak", want: "ghcr.io/bottlesdevs/bottles:feature-cpak"},
		{name: "release", release: "66.0", want: "ghcr.io/bottlesdevs/bottles:66.0"},
		{name: "commit", commit: "713cdd6d137b16a419d3bbb20f8b901fccc1927b", want: "ghcr.io/bottlesdevs/bottles:sha-713cdd6"},
		{name: "default", want: "ghcr.io/bottlesdevs/bottles:main"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveManifestImage(manifest, test.branch, test.release, test.commit)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveManifestImageKeepsStaticReference(t *testing.T) {
	manifest := &types.CpakManifest{Image: "ghcr.io/example/demo:stable"}
	got, err := resolveManifestImage(manifest, "feature/test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != manifest.Image {
		t.Fatalf("got %q, want %q", got, manifest.Image)
	}
}

func TestResolveManifestImageRejectsDigestTracking(t *testing.T) {
	manifest := &types.CpakManifest{
		Image:    "ghcr.io/example/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageRef: "source",
	}
	if _, err := resolveManifestImage(manifest, "main", "", ""); err == nil {
		t.Fatal("tracked a digest image")
	}
}
