package cpak

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/schollz/progressbar/v3"
)

// Pull pulls a remote image and unpacks it into the storage folder.
//
// Note: cpak does not offer a standard containers storage, it uses a custom
// storage based on the image layers.
func (c *Cpak) Pull(image string, cpakImageId string) (layers []string, ociConfig string, err error) {
	err = tools.ValidateImageName(image)
	if err != nil {
		return
	}

	// getting the v1.Image of the remote image
	img, err := crane.Pull(image, crane.WithContext(c.Ctx))
	if err != nil {
		return
	}

	// getting the image config
	ociConfigObj, err := img.ConfigFile()
	if err != nil {
		return
	}

	ociConfigBytes, err := json.Marshal(ociConfigObj)
	if err != nil {
		return
	}

	ociConfig = string(ociConfigBytes)

	// unpacking the image layers into the storage/images folder
	layerObjs, err := img.Layers()
	if err != nil {
		return
	}

	layers, err = c.unpackImageLayers(cpakImageId, img, layerObjs)
	if err != nil {
		return
	}

	return
}

// unpackImageLayers unpacks the image layers into the storage/images folder
// and returns the list of layers.
//
// Note: only the layers that are not already present in the storage are
// downloaded and unpacked.
func (c *Cpak) unpackImageLayers(digest string, image v1.Image, layerObjs []v1.Layer) (layers []string, err error) {
	availableLayers, err := c.GetAvailableLayers()
	if err != nil {
		return
	}

	for _, layer := range layerObjs {
		layerv1Hash, err := layer.Digest()
		if err != nil {
			return layers, err
		}
		layerDigest := strings.Split(layerv1Hash.String(), ":")[1]

		found := false
		for _, a := range availableLayers {
			if strings.Contains(a, layerDigest) {
				layers = append(layers, layerDigest)
				found = true
				break
			}
		}

		if found {
			fmt.Printf("Layer %s already present in the store, skipping..\n", layerDigest)
			continue
		}

		err = c.downloadLayer(image, layer, layerDigest)
		if err != nil {
			return layers, err
		}

		layers = append(layers, layerDigest)
	}

	return
}

func (c *Cpak) GetAvailableLayers() (layers []string, err error) {
	layersDir := c.GetInStoreDir("layers")

	_, err = os.Stat(layersDir)
	if err != nil {
		return nil, err
	}

	files, err := os.ReadDir(layersDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if file.IsDir() {
			layers = append(layers, filepath.Join(layersDir, file.Name()))
		}
	}

	return layers, nil
}

// func (c *Cpak) ensureApplicationLayers(layers []string) (err error) {
// 	availableLayers, err := c.GetAvailableLayers()
// 	if err != nil {
// 		return
// 	}

// 	for _, layer := range layers {
// 		found := false
// 		for _, a := range availableLayers {
// 			if strings.Contains(a, layer) {
// 				found = true
// 				break
// 			}
// 		}

// 		if !found {
// 			return fmt.Errorf("layer %s not found", layer)
// 		}
// 	}

// 	return
// }

func (c *Cpak) downloadLayer(image v1.Image, layer v1.Layer, digest string) (err error) {
	layerInCacheDir := c.GetInCacheDir(digest)
	layerContent, err := layer.Compressed()
	if err != nil {
		return
	}

	defer layerContent.Close()

	layerFile, err := os.Create(layerInCacheDir)
	if err != nil {
		return
	}

	defer layerFile.Close()

	layerSize, err := layer.Size()
	if err != nil {
		return
	}

	layerHash := digest[strings.Index(digest, ":")+1:][:12]

	bar := progressbar.NewOptions(int(layerSize),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "━",
			SaucerHead:    "╸",
			SaucerPadding: " ",
			BarStart:      "",
			BarEnd:        "",
		}),
		// the following add a new line after the progress bar
		progressbar.OptionSetWriter(io.MultiWriter(os.Stderr, os.Stderr)),
		progressbar.OptionSetDescription("Downloading "+layerHash),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionFullWidth(),
	)
	writer := io.MultiWriter(layerFile, bar)

	_, err = io.Copy(writer, layerContent)
	if err != nil {
		return
	}

	layerInStoreDir, err := c.GetInStoreDirMkdir("layers", digest)
	if err != nil {
		return
	}

	err = tools.TarUnpack(layerInCacheDir, layerInStoreDir)
	if err != nil {
		return
	}

	// dabadee deduplication is performed on a new namespace to avoid
	// permission issues
	cpakBinary, err := getCpakBinary()
	if err != nil {
		return
	}

	cmds := []string{}
	if isVerbose {
		cmds = append(cmds, "--debug")
	}
	cmds = append(cmds, []string{
		"--cgroupns=true",
		"--utsns=true",
		"--ipcns=true",
		"--copy-up=/etc",
		"--propagation=rslave",
		cpakBinary,
		"dedup",
	}...)
	if isVerbose {
		cmds = append(cmds, "--verbose")
	}
	cmds = append(cmds, "--path", layerInStoreDir)
	cmd := exec.Command(c.Options.RotlesskitBinPath, cmds...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Foreground: false,
		Setsid:     true,
	}

	err = cmd.Run()
	if err != nil {
		return
	}

	return
}
