package cpak

import (
	"path/filepath"
)

// GetNvidiaLibs finds the paths of the libraries needed by the
// GPU drivers to run.
//
// Note: this follows the same logic as the one used in the
// distrobox utility to find the nvidia libraries, see:
// https://github.com/mirkobrombin/distrobox/blob/9bea9498c58e367cea2f106492b5b5cbd8e6b713/distrobox-init#L1256
func GetNvidiaLibs() ([]string, error) {
	var nvidiaLibs []string

	// Looking for NVIDIA stuff in /etc
	nvidiaEtc, err := filepath.Glob("/etc/*nvidia*")
	if err != nil {
		return nil, err
	}

	nvidiaLibs = append(nvidiaLibs, nvidiaEtc...)

	// Looking for NVIDIA stuff in /usr
	nvidiaUsr, err := filepath.Glob("/usr/*nvidia*")
	if err != nil {
		return nil, err
	}

	nvidiaLibs = append(nvidiaLibs, nvidiaUsr...)

	// Looking for NVIDIA stuff in /usr/lib*
	nvidiaUsrLib, err := filepath.Glob("/usr/lib*/**/*nvidia*.so*")
	if err != nil {
		return nil, err
	}

	nvidiaLibs = append(nvidiaLibs, nvidiaUsrLib...)

	return nvidiaLibs, nil
}
