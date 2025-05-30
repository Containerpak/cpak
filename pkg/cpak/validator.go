package cpak

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Regular expressions for basic validation
var (
	versionPattern  = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)*(?:[-+][0-9A-Za-z.-]+)?$`)
	ociImagePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*/[A-Za-z0-9._-]+(?::[A-Za-z0-9._-]+)?$`)
	pathPattern     = regexp.MustCompile(`^(?:\./|\.\./|/)?(?:[A-Za-z0-9_\-\.]+/)*[A-Za-z0-9_\-\.]+$`)
	envPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=.+$`)
	cmdPattern      = regexp.MustCompile(`^[A-Za-z0-9_\-]+$`)
)

// ValidateManifestSyntax checks the syntax of each CpakManifest field.
func ValidateManifestSyntax(m *types.CpakManifest) error {
	var errs []string

	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, "name must be non-empty")
	}

	if strings.TrimSpace(m.Description) == "" {
		errs = append(errs, "description must be non-empty")
	}

	if !versionPattern.MatchString(m.Version) {
		errs = append(errs, fmt.Sprintf("invalid version %q: must be semver-like", m.Version))
	}

	if !ociImagePattern.MatchString(m.Image) {
		errs = append(errs, fmt.Sprintf("invalid image %q: must be a valid OCI image reference (e.g. registry/repo:tag)", m.Image))
	}

	for _, bin := range m.Binaries {
		if !strings.HasPrefix(bin, "/") {
			errs = append(errs, fmt.Sprintf("binary path %q must be an absolute path", bin))
		}
	}

	for _, de := range m.DesktopEntries {
		if !strings.HasSuffix(de, ".desktop") {
			errs = append(errs, fmt.Sprintf("desktop entry %q should end with .desktop", de))
		}
	}

	for _, dep := range m.Dependencies {
		if err := ValidateDependencyOrigin(dep.Origin); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if m.IdleTime < 0 {
		errs = append(errs, "idle_time must be non-negative")
	}

	// Validate override fields
	if err := ValidateOverrideSyntax(m.Override); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		return fmt.Errorf("manifest validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ValidateDependencyOrigin ensures the dependency origin matches
// "host/user/repo" format.
func ValidateDependencyOrigin(origin string) error {
	parts := strings.Split(origin, "/")
	if len(parts) != 3 {
		return fmt.Errorf("dependency origin %q must be in 'host/user/repo' format", origin)
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("dependency origin %q contains empty segment", origin)
		}
	}
	return nil
}

// ValidateOverrideSyntax checks syntax for override fields.
func ValidateOverrideSyntax(o types.Override) error {
	var errs []string

	// FsExtra: validate path-like syntax
	for _, p := range o.FsExtra {
		if !pathPattern.MatchString(p) {
			errs = append(errs, fmt.Sprintf("fsExtra path %q invalid syntax", p))
		}
	}

	// Env: validate KEY=VAL format
	for _, e := range o.Env {
		if !envPattern.MatchString(e) {
			errs = append(errs, fmt.Sprintf("env variable %q invalid, must be KEY=VAL", e))
		}
	}

	// AllowedHostCommands: simple names, no slashes
	for _, c := range o.AllowedHostCommands {
		if strings.Contains(c, "/") || !cmdPattern.MatchString(c) {
			errs = append(errs, fmt.Sprintf("allowedHostCommand %q invalid, must be simple command name", c))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("override validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
