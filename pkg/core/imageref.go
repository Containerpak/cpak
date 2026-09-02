/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ImageReference is a normalized OCI image reference. It lives here because a
// manifest is not valid until its image is, and that judgement has to be
// reachable everywhere a manifest is read.
//
// The messages still name oci, because that is the thing being parsed and it is
// the wording an author already sees when a reference is rejected.
type ImageReference struct {
	Registry   string
	Repository string
	Identifier string
	IsDigest   bool
}

// ParseImageReference validates and normalizes an OCI image reference.
func ParseImageReference(value string) (ImageReference, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n?#") {
		return ImageReference{}, fmt.Errorf("oci: invalid image reference")
	}

	name, identifier, isDigest, err := splitImageIdentifier(value)
	if err != nil {
		return ImageReference{}, err
	}
	parts := strings.Split(name, "/")
	registry := "index.docker.io"
	if len(parts) > 1 && isImageRegistry(parts[0]) {
		registry = strings.ToLower(parts[0])
		parts = parts[1:]
	}
	if registry == "docker.io" {
		registry = "index.docker.io"
	}
	if registry == "index.docker.io" && len(parts) == 1 {
		parts = append([]string{"library"}, parts...)
	}
	rawRepository := strings.Join(parts, "/")
	repository := strings.ToLower(rawRepository)
	if rawRepository != repository {
		return ImageReference{}, fmt.Errorf("oci: invalid image reference %q", value)
	}
	if !validImageRegistry(registry) || !repositoryPattern.MatchString(repository) {
		return ImageReference{}, fmt.Errorf("oci: invalid image reference %q", value)
	}
	return ImageReference{Registry: registry, Repository: repository, Identifier: identifier, IsDigest: isDigest}, nil
}

// ValidImageDigest reports whether a value is a digest cpak is willing to pin
// to, which is a sha256 one written in lowercase hexadecimal.
func ValidImageDigest(value string) bool {
	return digestPattern.MatchString(value)
}

func splitImageIdentifier(value string) (string, string, bool, error) {
	if strings.Count(value, "@") > 1 {
		return "", "", false, fmt.Errorf("oci: invalid image reference %q", value)
	}
	if name, digest, ok := strings.Cut(value, "@"); ok {
		if !digestPattern.MatchString(digest) {
			return "", "", false, fmt.Errorf("oci: unsupported image digest %q", digest)
		}
		lastSlash := strings.LastIndexByte(name, '/')
		if lastColon := strings.LastIndexByte(name, ':'); lastColon > lastSlash {
			if !tagPattern.MatchString(name[lastColon+1:]) {
				return "", "", false, fmt.Errorf("oci: invalid image tag %q", name[lastColon+1:])
			}
			name = name[:lastColon]
		}
		return name, digest, true, nil
	}
	identifier := "latest"
	name := value
	lastSlash := strings.LastIndexByte(value, '/')
	lastColon := strings.LastIndexByte(value, ':')
	if lastColon > lastSlash {
		name = value[:lastColon]
		identifier = value[lastColon+1:]
	}
	if !tagPattern.MatchString(identifier) {
		return "", "", false, fmt.Errorf("oci: invalid image tag %q", identifier)
	}
	return name, identifier, false, nil
}

func isImageRegistry(value string) bool {
	return value == "localhost" || strings.ContainsAny(value, ".:")
}

func validImageRegistry(value string) bool {
	if value == "" || strings.ContainsAny(value, " /@") {
		return false
	}
	return !strings.HasPrefix(value, ".") && !strings.HasSuffix(value, ".")
}

// ContextName returns the repository name without a tag or digest.
func (r ImageReference) ContextName() string {
	return r.Registry + "/" + r.Repository
}

// Name returns the normalized full reference.
func (r ImageReference) Name() string {
	separator := ":"
	if r.IsDigest {
		separator = "@"
	}
	return r.ContextName() + separator + r.Identifier
}

// WithTag returns the same repository with a new tag.
func (r ImageReference) WithTag(tag string) (ImageReference, error) {
	if !tagPattern.MatchString(tag) {
		return ImageReference{}, fmt.Errorf("oci: invalid image tag %q", tag)
	}
	r.Identifier = tag
	r.IsDigest = false
	return r, nil
}
