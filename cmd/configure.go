package cmd

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/rivo/tview"
	"github.com/spf13/cobra"
)

var tuiApp *tview.Application
var cpkInstance cpak.Cpak
var availableApps []types.Application
var selectedApp types.Application
var currentOverride types.Override
var manifestOverride types.Override
var initialUserOverride types.Override
var storeInstance *cpak.Store

const unsavedChangesModalName = "unsavedChangesModal"

var appConfigForm *tview.Form
var appConfigPages *tview.Pages

func NewConfigureCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configure [app_origin]",
		Short: "Configure overrides for installed cpak applications",
		Long:  `Access the configuration interface for installed cpak applications. You can specify an application by its origin.`,
		RunE:  runConfigure,
		Args:  cobra.MaximumNArgs(1),
	}
	return cmd
}

func runConfigure(cmd *cobra.Command, args []string) error {
	var err error
	cpkInstance, err = cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak: %w", err)
	}

	storeInstance, err = cpak.NewStore(cpkInstance.Options.StorePath)
	if err != nil {
		return fmt.Errorf("failed to open cpak store: %w", err)
	}
	defer storeInstance.Close()

	availableApps, err = storeInstance.GetApplications()
	if err != nil {
		return fmt.Errorf("failed to get installed applications: %w", err)
	}

	if len(availableApps) == 0 {
		fmt.Println("No cpak applications installed. Nothing to configure.")
		return nil
	}

	tuiApp = tview.NewApplication()
	appConfigPages = tview.NewPages()

	if len(args) == 1 {
		appOrigin := args[0]
		found := false
		for _, app := range availableApps {
			if app.Origin == appOrigin {
				selectedApp = app
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("application with origin '%s' not found", appOrigin)
		}
		loadOverridesForSelectedApp()
		createAppConfigurationView()
	} else {
		createAppSelectionView()
	}

	if err := tuiApp.Run(); err != nil {
		return fmt.Errorf("error running TUI: %w", err)
	}
	return nil
}

func loadOverridesForSelectedApp() {
	manifestOverride = selectedApp.ParsedOverride

	userOverride, err := cpak.LoadOverride(selectedApp.Origin, selectedApp.Version)
	if err == nil && !isOverrideEffectivelyEmpty(userOverride) {
		currentOverride = userOverride
		initialUserOverride = userOverride
	} else {
		currentOverride = manifestOverride
		initialUserOverride = types.NewOverride()
	}
}

func isOverrideEffectivelyEmpty(o types.Override) bool {

	defaultO := types.NewOverride()
	return o.SocketX11 == defaultO.SocketX11 &&
		o.SocketWayland == defaultO.SocketWayland &&
		o.SocketPulseAudio == defaultO.SocketPulseAudio &&
		o.SocketSessionBus == defaultO.SocketSessionBus &&
		o.SocketSystemBus == defaultO.SocketSystemBus &&
		o.SocketSshAgent == defaultO.SocketSshAgent &&
		o.SocketCups == defaultO.SocketCups &&
		o.SocketGpgAgent == defaultO.SocketGpgAgent &&
		o.SocketAtSpiBus == defaultO.SocketAtSpiBus &&
		o.DeviceDri == defaultO.DeviceDri &&
		o.DeviceKvm == defaultO.DeviceKvm &&
		o.DeviceShm == defaultO.DeviceShm &&
		o.DeviceAll == defaultO.DeviceAll &&
		o.FsHost == defaultO.FsHost &&
		o.FsHostEtc == defaultO.FsHostEtc &&
		o.FsHostHome == defaultO.FsHostHome &&
		len(o.FsExtra) == 0 &&
		len(o.Env) == 0 &&
		o.Network == defaultO.Network &&
		o.Process == defaultO.Process &&
		o.AsRoot == defaultO.AsRoot &&
		len(o.AllowedHostCommands) == 0
}

func createAppSelectionView() {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetTitle("Select Application to Configure").SetBorder(true)

	for i, app := range availableApps {
		appText := fmt.Sprintf("%s (%s)", app.Name, app.Origin)

		capturedApp := app
		list.AddItem(appText, capturedApp.Version, rune('a'+i), func() {
			selectedApp = capturedApp
			loadOverridesForSelectedApp()
			createAppConfigurationView()
		})
	}
	list.AddItem("Quit", "Exit Cpak Configure", 'q', func() {
		tuiApp.Stop()
	})

	tuiApp.SetRoot(list, true).SetFocus(list)
}

func rebuildAppConfigForm() {
	if appConfigForm == nil || appConfigPages == nil {

		return
	}
	appConfigForm.Clear(true)

	appConfigForm.AddCheckbox("Socket: X11", currentOverride.SocketX11, func(checked bool) { currentOverride.SocketX11 = checked })
	appConfigForm.AddCheckbox("Socket: Wayland", currentOverride.SocketWayland, func(checked bool) { currentOverride.SocketWayland = checked })
	appConfigForm.AddCheckbox("Socket: PulseAudio", currentOverride.SocketPulseAudio, func(checked bool) { currentOverride.SocketPulseAudio = checked })
	appConfigForm.AddCheckbox("Socket: Session Bus", currentOverride.SocketSessionBus, func(checked bool) { currentOverride.SocketSessionBus = checked })
	appConfigForm.AddCheckbox("Socket: System Bus", currentOverride.SocketSystemBus, func(checked bool) { currentOverride.SocketSystemBus = checked })
	appConfigForm.AddCheckbox("Socket: SSH Agent", currentOverride.SocketSshAgent, func(checked bool) { currentOverride.SocketSshAgent = checked })
	appConfigForm.AddCheckbox("Socket: CUPS", currentOverride.SocketCups, func(checked bool) { currentOverride.SocketCups = checked })
	appConfigForm.AddCheckbox("Socket: GPG Agent", currentOverride.SocketGpgAgent, func(checked bool) { currentOverride.SocketGpgAgent = checked })
	appConfigForm.AddCheckbox("Socket: AT-SPI Bus", currentOverride.SocketAtSpiBus, func(checked bool) { currentOverride.SocketAtSpiBus = checked })

	appConfigForm.AddDropDown("Device Access", []string{"None", "DRI", "KVM", "SHM", "DRI+KVM", "DRI+SHM", "KVM+SHM", "DRI+KVM+SHM", "All"}, 0, func(option string, optionIndex int) {
		currentOverride.DeviceDri, currentOverride.DeviceKvm, currentOverride.DeviceShm, currentOverride.DeviceAll = false, false, false, false
		switch option {
		case "DRI":
			currentOverride.DeviceDri = true
		case "KVM":
			currentOverride.DeviceKvm = true
		case "SHM":
			currentOverride.DeviceShm = true
		case "DRI+KVM":
			currentOverride.DeviceDri, currentOverride.DeviceKvm = true, true
		case "DRI+SHM":
			currentOverride.DeviceDri, currentOverride.DeviceShm = true, true
		case "KVM+SHM":
			currentOverride.DeviceKvm, currentOverride.DeviceShm = true, true
		case "DRI+KVM+SHM":
			currentOverride.DeviceDri, currentOverride.DeviceKvm, currentOverride.DeviceShm = true, true, true
		case "All":
			currentOverride.DeviceAll = true
		}
	})
	updateDeviceDropdownIndex(appConfigForm)

	appConfigForm.AddCheckbox("Filesystem: Host Root (ro)", currentOverride.FsHost, func(checked bool) { currentOverride.FsHost = checked })
	appConfigForm.AddCheckbox("Filesystem: Host /etc (ro)", currentOverride.FsHostEtc, func(checked bool) { currentOverride.FsHostEtc = checked })
	appConfigForm.AddCheckbox("Filesystem: Host Home (ro)", currentOverride.FsHostHome, func(checked bool) { currentOverride.FsHostHome = checked })
	appConfigForm.AddCheckbox("Network Access", currentOverride.Network, func(checked bool) { currentOverride.Network = checked })
	appConfigForm.AddCheckbox("Share Process Namespace", currentOverride.Process, func(checked bool) { currentOverride.Process = checked })
	appConfigForm.AddCheckbox("Run as Root (in container)", currentOverride.AsRoot, func(checked bool) { currentOverride.AsRoot = checked })

	addEditableList(appConfigForm, appConfigPages, "Filesystem: Extra Paths (ro)", &currentOverride.FsExtra)
	addEditableList(appConfigForm, appConfigPages, "Environment Variables (KEY=VAL)", &currentOverride.Env)
	addEditableList(appConfigForm, appConfigPages, "Allowed Host Commands", &currentOverride.AllowedHostCommands)

	appConfigForm.AddButton("Save", func() {
		err := cpak.SaveOverride(currentOverride, selectedApp.Origin, selectedApp.Version)
		if err != nil {
			showModal(appConfigPages, "Error", fmt.Sprintf("Failed to save override: %v", err), []string{"OK"}, func(buttonIndex int, buttonLabel string) {
				appConfigPages.RemovePage("ErrorModal")
			})
		} else {
			initialUserOverride = currentOverride
			showModal(appConfigPages, "Success", "Overrides saved successfully!", []string{"OK"}, func(buttonIndex int, buttonLabel string) {
				appConfigPages.RemovePage("SuccessModal")
			})
		}
	})
	appConfigForm.AddButton("Reset to Manifest", func() {
		currentOverride = manifestOverride
		rebuildAppConfigForm()
	})
	appConfigForm.AddButton("Reset to cpak Defaults", func() {
		currentOverride = types.NewOverride()
		rebuildAppConfigForm()
	})
	appConfigForm.AddButton("Back to App List / Quit", func() {
		if hasUnsavedChanges() {
			showUnsavedChangesModal(appConfigPages, func() { tuiApp.Stop() }, createAppSelectionView)
		} else {
			createAppSelectionView()
		}
	})
	appConfigForm.SetBorder(true).SetTitle(fmt.Sprintf("Configure: %s (%s)", selectedApp.Name, selectedApp.Origin)).SetTitleAlign(tview.AlignLeft)
}

func createAppConfigurationView() {
	appConfigForm = tview.NewForm()

	rebuildAppConfigForm()

	mainFrame := tview.NewFrame(appConfigForm).
		SetBorders(0, 0, 0, 0, 0, 0).
		AddText("Use Tab/Shift-Tab, Enter to toggle/select, Esc for modals/back.", false, tview.AlignCenter, tcell.ColorWhiteSmoke)

	appConfigPages.AddPage("form", mainFrame, true, true)
	tuiApp.SetRoot(appConfigPages, true).SetFocus(appConfigForm)
}

func updateDeviceDropdownIndex(form *tview.Form) {
	var index int
	if currentOverride.DeviceAll {
		index = 8
	} else if currentOverride.DeviceDri && currentOverride.DeviceKvm && currentOverride.DeviceShm {
		index = 7
	} else if currentOverride.DeviceKvm && currentOverride.DeviceShm {
		index = 6
	} else if currentOverride.DeviceDri && currentOverride.DeviceShm {
		index = 5
	} else if currentOverride.DeviceDri && currentOverride.DeviceKvm {
		index = 4
	} else if currentOverride.DeviceShm {
		index = 3
	} else if currentOverride.DeviceKvm {
		index = 2
	} else if currentOverride.DeviceDri {
		index = 1
	} else {
		index = 0
	}

	for i := 0; i < form.GetFormItemCount(); i++ {
		item := form.GetFormItem(i)
		if dd, ok := item.(*tview.DropDown); ok && dd.GetLabel() == "Device Access" {
			dd.SetCurrentOption(index)
			break
		}
	}
}

func rebuildListEditorView(listView *tview.List, textInput *tview.InputField, listData *[]string, pages *tview.Pages, form *tview.Form, baseLabel string) {
	listView.Clear()

	for i, item := range *listData {
		localItem := item
		localIndex := i
		listView.AddItem(localItem, "", rune(0), func() {
			*listData = append((*listData)[:localIndex], (*listData)[localIndex+1:]...)
			rebuildListEditorView(listView, textInput, listData, pages, form, baseLabel)
		})
	}
	textInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEsc {
			pages.HidePage("listEditor_" + strings.ReplaceAll(baseLabel, " ", "_"))
			pages.SwitchToPage("form")
			tuiApp.SetFocus(form)
			return nil
		}
		return event
	})
	listView.AddItem("Add New (type below and press Enter)", "", 'n', func() {
		tuiApp.SetFocus(textInput)
	})
	listView.AddItem("Done", "Return to main form", 'd', func() {
		pages.HidePage("listEditor_" + strings.ReplaceAll(baseLabel, " ", "_"))
		pages.SwitchToPage("form")
		tuiApp.SetFocus(form)

		newButtonTextDisplay := "<empty>"
		if len(*listData) > 0 {
			newButtonTextDisplay = strings.Join(*listData, "; ")
		}
		for i := 0; i < form.GetButtonCount(); i++ {
			btn := form.GetButton(i)
			if strings.HasPrefix(btn.GetLabel(), baseLabel) {
				btn.SetLabel(baseLabel + ": " + newButtonTextDisplay + " [Edit]")
				break
			}
		}
	})
	if listView.GetItemCount() > 0 {
		if listView.GetCurrentItem() < 0 || listView.GetCurrentItem() >= listView.GetItemCount() {
			listView.SetCurrentItem(0)
		}
	}
}

func addEditableList(form *tview.Form, pages *tview.Pages, label string, listData *[]string) {
	baseLabel := label
	display := func() string {
		if len(*listData) == 0 {
			return "<empty>"
		}
		return strings.Join(*listData, "; ")
	}

	var listEditorView *tview.List
	var textInput *tview.InputField
	var flexContainer *tview.Flex

	form.AddButton(label+": "+display()+" [Edit]", func() {
		listEditorView = tview.NewList().ShowSecondaryText(false)
		textInput = tview.NewInputField().SetLabel("New Item: ")

		textInput.SetDoneFunc(func(key tcell.Key) {
			if key == tcell.KeyEnter {
				newItem := textInput.GetText()
				if newItem != "" {
					*listData = append(*listData, newItem)
					textInput.SetText("")
					rebuildListEditorView(listEditorView, textInput, listData, pages, form, baseLabel)
				}
			}
		})

		flexContainer = tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(listEditorView, 0, 1, true).
			AddItem(textInput, 3, 0, false)

		flexContainer.SetBorder(true).SetTitle("Edit " + label)
		pageName := "listEditor_" + strings.ReplaceAll(label, " ", "_")
		rebuildListEditorView(listEditorView, textInput, listData, pages, form, baseLabel)

		pages.AddPage(pageName, modal(flexContainer, 80, 20), true, true)
		tuiApp.SetFocus(listEditorView)
	})
}

func modal(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height, 1, true).
			AddItem(nil, 0, 1, false), width, 1, true).
		AddItem(nil, 0, 1, false)
}

func showModal(pages *tview.Pages, title, text string, buttons []string, callback func(buttonIndex int, buttonLabel string)) {
	modalView := tview.NewModal().
		SetText(text).
		AddButtons(buttons).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			callback(buttonIndex, buttonLabel)
		})
	modalView.SetTitle(title).SetBorder(true)
	modalName := strings.ReplaceAll(title, " ", "") + "Modal"
	pages.AddPage(modalName, modal(modalView, 60, 10), true, true)
	tuiApp.SetFocus(modalView)
}

func hasUnsavedChanges() bool {
	var baseOverride types.Override
	if !isOverrideEffectivelyEmpty(initialUserOverride) {
		baseOverride = initialUserOverride
	} else {
		baseOverride = manifestOverride
	}

	if currentOverride.SocketX11 != baseOverride.SocketX11 ||
		currentOverride.SocketWayland != baseOverride.SocketWayland ||
		currentOverride.SocketPulseAudio != baseOverride.SocketPulseAudio ||
		currentOverride.SocketSessionBus != baseOverride.SocketSessionBus ||
		currentOverride.SocketSystemBus != baseOverride.SocketSystemBus ||
		currentOverride.SocketSshAgent != baseOverride.SocketSshAgent ||
		currentOverride.SocketCups != baseOverride.SocketCups ||
		currentOverride.SocketGpgAgent != baseOverride.SocketGpgAgent ||
		currentOverride.SocketAtSpiBus != baseOverride.SocketAtSpiBus ||
		currentOverride.DeviceDri != baseOverride.DeviceDri ||
		currentOverride.DeviceKvm != baseOverride.DeviceKvm ||
		currentOverride.DeviceShm != baseOverride.DeviceShm ||
		currentOverride.DeviceAll != baseOverride.DeviceAll ||
		currentOverride.FsHost != baseOverride.FsHost ||
		currentOverride.FsHostEtc != baseOverride.FsHostEtc ||
		currentOverride.FsHostHome != baseOverride.FsHostHome ||
		currentOverride.Network != baseOverride.Network ||
		currentOverride.Process != baseOverride.Process ||
		currentOverride.AsRoot != baseOverride.AsRoot ||
		!slicesAreEqual(currentOverride.FsExtra, baseOverride.FsExtra) ||
		!slicesAreEqual(currentOverride.Env, baseOverride.Env) ||
		!slicesAreEqual(currentOverride.AllowedHostCommands, baseOverride.AllowedHostCommands) {
		return true
	}
	return false
}

func slicesAreEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	if (a == nil && b == nil) || (len(a) == 0 && len(b) == 0) {
		return true
	}

	if (a == nil && len(b) == 0) || (len(a) == 0 && b == nil) {
		return true
	}

	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

func showUnsavedChangesModal(pages *tview.Pages, quitFunc func(), backToListFunc func()) {
	modalView := tview.NewModal().
		SetText("You have unsaved changes. Do you want to discard them?").
		AddButtons([]string{"Discard and Proceed", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			pages.RemovePage(unsavedChangesModalName)
			if buttonLabel == "Discard and Proceed" {
				if backToListFunc != nil {
					backToListFunc()
				} else {
					quitFunc()
				}
			} else {
				if appConfigForm != nil {
					tuiApp.SetFocus(appConfigForm)
				}
			}
		})
	modalView.SetBorder(true).SetTitle("Unsaved Changes")
	pages.AddPage(unsavedChangesModalName, modal(modalView, 60, 7), true, true)
	tuiApp.SetFocus(modalView)
}
