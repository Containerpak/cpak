package cmd

/*
cpak install <remote> --branch? --release? --commit?
*/

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/spf13/cobra"
)

func NewInstallCommand() *cobra.Command {
	var branch string
	var release string
	var commit string

	cmd := &cobra.Command{
		Use:   "install <remote>",
		Short: "Install a package from a remote Git repository",
		Args:  cobra.ExactArgs(1),
		RunE:  InstallPackage,
	}

	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Specify a branch")
	cmd.Flags().StringVarP(&release, "release", "r", "", "Install a specific release")
	cmd.Flags().StringVarP(&commit, "commit", "c", "", "Specify a commit")

	return cmd
}

func installError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while installing cpak: %s", iErr)
	return
}

func InstallPackage(cmd *cobra.Command, args []string) (err error) {
	remote := args[0]
	fmt.Println("Installing cpak from remote:", remote)

	branch, _ := cmd.Flags().GetString("branch")
	release, _ := cmd.Flags().GetString("release")
	commit, _ := cmd.Flags().GetString("commit")

	cpak, err := cpak.NewCpak()
	if err != nil {
		return installError(err)
	}

	versionParams := []string{branch, release, commit}
	versionParamsCount := 0
	for _, versionParam := range versionParams {
		if versionParam != "" {
			versionParamsCount++
		}
	}
	if versionParamsCount > 1 {
		return fmt.Errorf("more than one version parameter specified")
	}

	// if all version parameters are empty, we default to the main branch
	// assuming it is the default branch of the repository
	if versionParamsCount == 0 {
		fmt.Println("No version specified, using main branch if available")
		branch = "main"
	}

	manifest, err := cpak.FetchManifest(remote, branch, release, commit)
	if err != nil {
		return err
	}

	fmt.Println("\nThe following cpak(s) will be installed:")
	fmt.Printf("  - %s: %s\n", manifest.Name, manifest.Description)
	fmt.Println()

	fmt.Println("The following will be exported:")
	for _, binary := range manifest.Binaries {
		fmt.Printf("  - (binary) %s\n", binary)
	}
	for _, dependency := range manifest.DesktopEntries {
		fmt.Printf("  - (desktop entry) %s\n", dependency)
	}
	fmt.Println()

	fmt.Println("The following dependencies will be installed:")
	for _, dependency := range manifest.Dependencies {
		fmt.Printf("  - %s\n", dependency)
	}
	fmt.Println()

	fmt.Println("The following permissions will be granted:")
	fmt.Println("  - socket-x11:", manifest.Override.SocketX11)
	fmt.Println("  - socket-wayland:", manifest.Override.SocketWayland)
	fmt.Println("  - socket-pulseaudio:", manifest.Override.SocketPulseAudio)
	fmt.Println("  - socket-session-bus:", manifest.Override.SocketSessionBus)
	fmt.Println("  - socket-system-bus:", manifest.Override.SocketSystemBus)
	fmt.Println("  - socket-ssh-agent:", manifest.Override.SocketSshAgent)
	fmt.Println("  - socket-cups:", manifest.Override.SocketCups)
	fmt.Println("  - socket-gpg-agent:", manifest.Override.SocketGpgAgent)
	fmt.Println("  - device-dri:", manifest.Override.DeviceDri)
	fmt.Println("  - device-kvm:", manifest.Override.DeviceKvm)
	fmt.Println("  - device-shm:", manifest.Override.DeviceShm)
	fmt.Println("  - device-all:", manifest.Override.DeviceAll)
	fmt.Println("  - fs-host:", manifest.Override.FsHost)
	fmt.Println("  - fs-host-etc:", manifest.Override.FsHostEtc)
	fmt.Println("  - fs-host-home:", manifest.Override.FsHostHome)
	fmt.Println("  - fs-extra:", manifest.Override.FsExtra)
	fmt.Println("  - env:", manifest.Override.Env)
	fmt.Println("  - network:", manifest.Override.Network)
	fmt.Println("  - process:", manifest.Override.Process)
	fmt.Println("  - as-root:", manifest.Override.AsRoot)
	fmt.Println()

	confirm := tools.ConfirmOperation("Do you want to continue?")
	if !confirm {
		return
	}

	return cpak.InstallCpak(remote, manifest)

}
