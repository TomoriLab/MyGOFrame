package presentation

import (
	"errors"
	"strings"

	recording "github.com/TomoriLab/MyGOFrame/src/internal/recording/domain"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/egoist/mygo/ui"
)

func (application *applicationState) acceptPresets(document session.PresetDocument, preferred string) {
	if document.SelectedName != "" {
		preferred = document.SelectedName
	}
	application.presets = document.Presets
	application.selectedPreset = ""
	for _, preset := range document.Presets {
		if preset.Name == preferred {
			application.selectedPreset = preset.Name
		}
	}
	if application.selectedPreset == "" && len(document.Presets) > 0 {
		application.selectedPreset = document.Presets[0].Name
	}
	application.presetDeletePending = false
}

func (application *applicationState) loadPresets() {
	document, err := application.services.Presets(session.PresetRequest{Operation: "list"})
	if err != nil {
		application.presetError = err.Error()
		return
	}
	application.acceptPresets(document, application.selectedPreset)
}

func (application *applicationState) currentPreset(name string) (session.CapturePreset, error) {
	mode, valid := application.selection.selected()
	path := application.selectedDevice()
	if !valid || path == "" {
		return session.CapturePreset{}, errors.New("Select a capture device and an available video mode before saving a preset.")
	}
	source, output := application.audioIDs()
	deviceName := ""
	for _, device := range application.devices {
		if device.Path == path {
			deviceName = device.Name
		}
	}
	recording, monitoring := application.recordingConfig, application.monitorAudio
	return session.CapturePreset{Recording: &recording, MonitorEnabled: &monitoring, Name: name, DevicePath: path, DeviceName: deviceName, Mode: mode,
		ColorRange: application.colorRange, Backend: application.videoBackend,
		AudioSource: source, AudioOutput: output, Volume: application.volume / 100,
		Muted: application.muted, CaptureWindow: application.presentation != nil}, nil
}

func (application *applicationState) presetOperation(request session.PresetRequest) {
	if application.presetBusy || application.window == nil || application.closing {
		return
	}
	application.presetBusy = true
	done := make(chan struct{})
	application.presetDone = done
	application.presetError, application.presetNotice = "", ""
	preferred := application.selectedPreset
	if request.Preset != nil {
		preferred = strings.TrimSpace(request.Preset.Name)
	}
	// Persistence has a separate goroutine so a slow configuration filesystem cannot block frame delivery.
	go func() {
		defer close(done)
		document, err := application.services.Presets(request)
		application.window.Update(func() {
			if application.closing {
				return
			}
			application.presetBusy = false
			if err != nil {
				application.presetError = err.Error()
				application.debugEvent("preset.failed", err.Error())
				return
			}
			application.debugEvent("preset."+request.Operation, request.Name)
			application.acceptPresets(document, preferred)
			switch request.Operation {
			case "save":
				application.presetNotice = "Preset saved."
			case "update":
				application.presetNotice = "Preset updated."
			case "delete":
				application.presetNotice = "Preset deleted."
			case "select":
				application.presetNotice = "Preset selected."
			default:
				application.presetNotice = "Preset list reloaded."
			}
			application.invalidatePresentation()
		})
	}()
}

func (application *applicationState) savePreset(update bool) {
	name := application.presetName
	operation := "save"
	if update {
		name, operation = application.selectedPreset, "update"
	}
	preset, err := application.currentPreset(name)
	if err != nil {
		application.presetError = err.Error()
		return
	}
	application.presetOperation(session.PresetRequest{Operation: operation, Preset: &preset})
}

func (application *applicationState) applyPreset() {
	if application.running || application.busy || application.presetBusy || application.status.Recording.Active() {
		return
	}
	var selected *session.CapturePreset
	for _, preset := range application.presets {
		if preset.Name == application.selectedPreset {
			copy := preset
			selected = &copy
			break
		}
	}
	if selected == nil {
		return
	}
	application.presetError, application.presetNotice = "", ""
	application.submit("Applying capture preset…", func(worker *captureWorker) (func(*applicationState), error) {
		previous, _ := worker.engine.Snapshot()
		result, err := worker.engine.ApplyPreset(*selected)
		if err == nil {
			worker.setAudioControls(session.AudioControls{Volume: selected.Volume, Muted: selected.Muted})
		}
		snapshot, _ := worker.engine.Snapshot()
		return func(application *applicationState) {
			if err != nil {
				application.presetError = err.Error()
				if snapshot.CommandID != previous.CommandID && !snapshot.Audio.Running {
					application.audioSource = audioOff
				}
				return
			}
			application.applyPresetSelection(result)
		}, err
	})
}

func (application *applicationState) applyPresetSelection(result session.PresetResolution) {
	preset := result.Preset
	application.recordingConfig = recording.DefaultRecordingConfiguration()
	if preset.Recording != nil {
		application.recordingConfig = *preset.Recording
	}
	application.monitorAudio = preset.MonitorEnabled == nil || *preset.MonitorEnabled
	application.recordingGPU = "Automatic"
	for _, device := range application.recordingDevices {
		if device.ID == application.recordingConfig.GPUID {
			application.recordingGPU = recordingDeviceLabel(device)
		}
	}
	if application.recordingConfig.GPUID != "auto" && application.recordingGPU == "Automatic" {
		application.recordingGPU = "Unavailable saved GPU"
	}
	application.devices, application.audioDevices = result.Devices, result.AudioDevices
	for _, device := range result.Devices {
		if device.Path == preset.DevicePath {
			application.device = videoLabel(device)
		}
	}
	application.selection.format = result.Mode.Format
	application.selection.resolution, application.selection.frameRate = result.Mode.Resolution(), result.Mode.FrameRate()
	application.selection.setModes(result.Modes)
	application.colorRange, application.videoBackend = preset.ColorRange, preset.Backend
	application.audioSource, application.audioOutput = audioOff, defaultOutput
	for _, device := range result.AudioDevices.Sources {
		if device.ID == preset.AudioSource {
			application.audioSource = audioLabel(device)
		}
	}
	for _, device := range result.AudioDevices.Outputs {
		if device.ID == preset.AudioOutput {
			application.audioOutput = audioLabel(device)
		}
	}
	application.volume, application.muted = preset.Volume*100, preset.Muted
	application.presetNotice = "Preset applied. Start video when ready."
	if preset.CaptureWindow {
		application.openPresentation()
	} else {
		application.closePresentation()
	}
}

func (application *applicationState) presetControls(context *ui.Context) {
	disabled := application.busy || application.presetBusy
	ui.Text(context, text(context, "Settings presets")).FontSize(16).FontWeight(600)
	helpText(context, "Save video, audio, volume, and capture-window settings for each console.")
	names := make([]string, 0, len(application.presets))
	for _, preset := range application.presets {
		names = append(names, preset.Name)
	}
	field(context, "Saved preset", func() {
		if ui.Select(context.Key("preset-selection"), &application.selectedPreset, names).FillWidth().Label(text(context, "Saved preset")).Disabled(disabled || len(names) == 0).Changed() {
			application.presetDeletePending = false
			application.presetNotice = ""
		}
	})
	if len(names) == 0 {
		helpText(context, "No saved presets yet.")
	}
	if ui.PrimaryButton(context.Key("apply-preset"), text(context, "Apply preset")).FillWidth().Disabled(disabled || application.running || application.status.Recording.Active() || application.selectedPreset == "").Clicked() {
		application.applyPreset()
	}
	helpText(context, "Stop video before applying. Audio monitoring follows the saved selection.")
	field(context, "New preset name", func() {
		ui.TextInput(context.Key("preset-name"), &application.presetName).FillWidth().Label(text(context, "Preset name input")).Disabled(disabled)
	})
	_, valid := application.selection.selected()
	if ui.Button(context.Key("save-preset"), text(context, "Save new preset")).FillWidth().Disabled(disabled || !valid || strings.TrimSpace(application.presetName) == "").Clicked() {
		application.savePreset(false)
	}
	if ui.Button(context.Key("update-preset"), text(context, "Update preset")).FillWidth().Disabled(disabled || !valid || application.selectedPreset == "").Clicked() {
		application.savePreset(true)
	}
	helpText(context, "Update preset replaces the selected preset with the current settings.")
	if application.presetDeletePending {
		helpText(context, "Delete the selected preset?")
		if ui.Button(context.Key("confirm-delete-preset"), text(context, "Confirm delete")).FillWidth().Disabled(disabled).Clicked() {
			application.presetOperation(session.PresetRequest{Operation: "delete", Name: application.selectedPreset})
		}
		if ui.Button(context.Key("cancel-delete-preset"), text(context, "Cancel")).FillWidth().Disabled(disabled).Clicked() {
			application.presetDeletePending = false
		}
	} else if ui.Button(context.Key("delete-preset"), text(context, "Delete preset")).FillWidth().Disabled(disabled || application.selectedPreset == "").Clicked() {
		application.presetDeletePending = true
	}
	if ui.Button(context.Key("reload-presets"), text(context, "Reload presets")).FillWidth().Disabled(disabled).Clicked() {
		application.presetOperation(session.PresetRequest{Operation: "list"})
	}
	if application.presetBusy {
		helpText(context, "Saving preset changes…")
	}
	if application.presetNotice != "" {
		helpText(context, application.presetNotice)
	}
	if application.presetError != "" {
		ui.Text(context, text(context, application.presetError)).FontSize(12).TextColor(context.Theme().Danger)
	}
}

func (application *applicationState) activateSelectedPreset(start bool, previousSelection string) {
	if application.busy || application.presetBusy || application.status.Recording.Active() || application.worker == nil {
		return
	}
	var selected *session.CapturePreset
	for _, preset := range application.presets {
		if preset.Name == application.selectedPreset {
			value := preset
			selected = &value
			break
		}
	}
	if selected == nil {
		return
	}
	application.presetError = ""
	application.gpuFailure = nil
	application.gpuFallbackPending = false
	application.clearPreview()
	application.submit("Switching capture preset…", func(worker *captureWorker) (func(*applicationState), error) {
		worker.frames.clear()
		worker.afterSequence = 0
		result, err := worker.engine.ActivatePreset(*selected, start)
		if err == nil {
			worker.setAudioControls(session.AudioControls{Volume: selected.Volume, Muted: selected.Muted})
		}
		return func(application *applicationState) {
			application.clearPreview()
			if err != nil {
				application.presetError = err.Error()
				application.selectedPreset = previousSelection
				return
			}
			application.applyPresetSelection(result)
			application.presetNotice = "Preset selected."
			application.presetOperation(session.PresetRequest{Operation: "select", Name: selected.Name})
		}, err
	})
}

func (application *applicationState) quickPresetBar(context *ui.Context, compact bool, width float32) {
	if len(application.presets) == 0 || (!compact && width >= 1120) {
		return
	}
	padding := float32(16)
	if compact {
		padding = 8
	}
	stacked := width < 560
	selectorWidth := min(float32(330), max(float32(176), width-2*padding-16-128-8-captureActionWidth(context)))
	if stacked {
		selectorWidth = width - 2*padding
	}
	ui.Row(context).FillWidth().Shrink(0).Padding(0, padding, 12).Children(func() {
		application.quickPresetControls(context, selectorWidth, false, 16, stacked)
	})
}

func (application *applicationState) quickPresetControls(context *ui.Context, selectorWidth float32, showLabel bool, gap float32, stacked bool) {
	disabled := application.busy || application.presetBusy
	names := make([]string, 0, len(application.presets))
	selectedExists := false
	for _, preset := range application.presets {
		names = append(names, preset.Name)
		if preset.Name == application.selectedPreset {
			selectedExists = true
		}
	}
	actionWidth := captureActionWidth(context)
	actionsWidth := 128 + 8 + actionWidth
	groupWidth := selectorWidth + gap + actionsWidth
	if showLabel {
		groupWidth += presetLabelWidth(context) + gap
	}
	group := ui.Row(context).Width(groupWidth).Height(32).Shrink(0).Gap(gap).AlignItems(ui.Center).Label(text(context, "Quick capture controls"))
	if stacked {
		group.Column().Width(selectorWidth).Height(72).Gap(8).AlignItems(ui.Stretch)
	}
	group.Children(func() {
		if showLabel {
			ui.Text(context, text(context, "Preset")).Width(presetLabelWidth(context)).Shrink(0).FontSize(12).TextColor(context.Theme().TextMuted).SingleLine()
		}
		previous := application.selectedPreset
		if ui.Select(context.Key("quick-preset"), &application.selectedPreset, names).Width(selectorWidth).Height(32).Shrink(0).Label(text(context, "Quick preset selection")).Tooltip(text(context, "Capture preset")).Disabled(disabled || application.status.Recording.Active()).Changed() {
			application.activateSelectedPreset(false, previous)
		}
		actions := func() {
			application.recordingCommandButton(context, false)
			label := "Start video"
			if application.running {
				label = "Stop video"
			}
			buttonDisabled := disabled || (!application.running && (!selectedExists || application.status.Recording.Active()))
			if ui.PrimaryButton(context.Key("quick-video-toggle"), text(context, label)).Size(actionWidth, 32).Label(text(context, "Preset video action")).Shrink(0).Disabled(buttonDisabled).Clicked() {
				if application.running {
					application.stopVideo()
				} else {
					application.activateSelectedPreset(true, application.selectedPreset)
				}
			}
		}
		if stacked {
			ui.Row(context).FillWidth().Height(32).Shrink(0).Gap(8).Justify(ui.End).Children(actions)
		} else {
			ui.Row(context).Width(actionsWidth).Height(32).Shrink(0).Gap(8).Children(actions)
		}
	})
}
