package cpak

import (
	"os"
	"path/filepath"
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
