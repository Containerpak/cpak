/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GetInStoreDir returns the path to the given subdirectory in the store.
//
// Note: this does not check if the directory exists, it just returns it.
func (c *Cpak) GetInStoreDir(sub string, args ...string) string {
	return filepath.Join(c.Options.StorePath, sub, filepath.Join(args...))
}

// GetInStoreDirMkdir returns the path to the given subdirectory in the store
// and creates it if it does not exist.
func (c *Cpak) GetInStoreDirMkdir(sub string, args ...string) (path string, err error) {
	path = c.GetInStoreDir(sub, args...)
	realPath := path
	if filepath.Ext(path) != "" {
		path = filepath.Dir(path)
	}
	err = os.MkdirAll(realPath, 0755)

	if sub == "states" && len(args) == 1 {
		_, err = c.GetInStoreDirMkdir("states", args[0], "up")
		if err != nil {
			return
		}
		_, err = c.GetInStoreDirMkdir("states", args[0], "work")
		if err != nil {
			return
		}
	}

	return
}

// GetInCacheDir returns the path to the given subdirectory in the cache.
//
// Note: this does not check if the directory exists, it just returns it.
func (c *Cpak) GetInCacheDir(args ...string) string {
	return filepath.Join(c.Options.CachePath, filepath.Join(args...))
}

// GetInCacheDirMkdir returns the path to the given subdirectory in the cache
// and creates it if it does not exist.
func (c *Cpak) GetInCacheDirMkdir(args ...string) (path string, err error) {
	path = c.GetInCacheDir(args...)
	realPath := path
	if filepath.Ext(path) != "" {
		path = filepath.Dir(path)
	}

	err = os.MkdirAll(realPath, 0755)
	return
}

// Following funcs are just wrappers around GetInStoreDir and GetInCacheDir
// for convenience

// GetInStoreContainersDir returns the path to the containers directory in
// the store.
//
// Note: this does not check if the directory exists, it just returns it.
func (c *Cpak) GetInStoreLayersDir(args ...string) string {
	return c.GetInStoreDir("layers", args...)
}

// GetInManifestsDir returns the path to the manifests directory in the store.
//
// Note: this does not check if the directory exists, it just returns it.
func (c *Cpak) GetInManifestsDir(origin string, args ...string) string {
	args = append([]string{origin}, args...)
	return filepath.Join(c.Options.ManifestsPath, filepath.Join(args...))
}

// GetInManifestsDirMkdir returns the path to the containers directory in
// the manifests and creates it if it does not exist.
func (c *Cpak) GetInManifestsDirMkdir(origin string, args ...string) (path string, err error) {
	cpakLocalName, err := getCpakLocalName(origin)
	if err != nil {
		return
	}

	path = c.GetInManifestsDir(cpakLocalName, args...)
	realPath := path
	if filepath.Ext(path) != "" {
		path = filepath.Dir(path)
	}

	err = os.MkdirAll(realPath, 0755)
	return
}

// getCpakLocalName returns the local name of the cpak.
func getCpakLocalName(origin string) (cpakLocalName string, err error) {
	originItems := strings.Split(origin, "/")
	if len(originItems) != 3 {
		return "", fmt.Errorf("invalid origin: %s", origin)
	}

	cpakLocalName = filepath.Join(originItems[0], originItems[1], originItems[2])
	return
}
