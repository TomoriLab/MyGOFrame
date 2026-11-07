package main

import (
	"github.com/TomoriLab/MyGOFrame/internal/branding"
	"github.com/egoist/mygo/ui"
)

type fluentPalette struct {
	theme             *ui.Theme
	card, stroke      ui.Color
	successBackground ui.Color
	successForeground ui.Color
}

var fluentLight = makeFluentPalette(false)
var fluentDark = makeFluentPalette(true)

func fluentAppearance(dark bool) *fluentPalette {
	if dark {
		return &fluentDark
	}
	return &fluentLight
}

func makeFluentPalette(dark bool) fluentPalette {
	theme := ui.LightTheme()
	theme.Spacing, theme.Radius, theme.FontSize = 4, 4, 14
	theme.Background = ui.Hex("#f5f5f5")
	theme.Surface, theme.SurfaceHover, theme.SurfacePressed = ui.Hex("#ffffff"), ui.Hex("#f5f5f5"), ui.Hex("#e0e0e0")
	theme.Border, theme.Text, theme.TextMuted = ui.Hex("#d1d1d1"), ui.Hex("#242424"), ui.Hex("#616161")
	theme.Accent, theme.AccentHover, theme.AccentPressed = ui.Hex("#0f6cbd"), ui.Hex("#115ea3"), ui.Hex("#0c3b5e")
	theme.AccentText, theme.Focus = ui.Hex("#ffffff"), ui.Hex("#242424")
	theme.Danger, theme.Success, theme.Warning = ui.Hex("#b10e1e"), ui.Hex("#0e700e"), ui.Hex("#8a3707")
	theme.Selection = ui.RGBA(15, 108, 189, 0.2)
	palette := fluentPalette{theme: theme, card: ui.Hex("#ffffff"), stroke: ui.Hex("#e0e0e0"), successBackground: ui.Hex("#f1faf1"), successForeground: theme.Success}
	if dark {
		theme.Dark = true
		theme.Background = ui.Hex("#1f1f1f")
		theme.Surface, theme.SurfaceHover, theme.SurfacePressed = ui.Hex("#333333"), ui.Hex("#3d3d3d"), ui.Hex("#1f1f1f")
		theme.Border, theme.Text, theme.TextMuted = ui.Hex("#666666"), ui.Hex("#ffffff"), ui.Hex("#bdbdbd")
		theme.Accent, theme.AccentHover, theme.AccentPressed = ui.Hex("#115ea3"), ui.Hex("#0f6cbd"), ui.Hex("#0c3b5e")
		theme.Focus, theme.Danger, theme.Success, theme.Warning = ui.Hex("#ffffff"), ui.Hex("#f1bbbc"), ui.Hex("#9fd89f"), ui.Hex("#f2d5c1")
		theme.Selection, theme.Scrollbar = ui.RGBA(71, 158, 245, 0.3), ui.RGBA(255, 255, 255, 0.35)
		palette.card, palette.stroke = ui.Hex("#292929"), ui.Hex("#424242")
		palette.successBackground, palette.successForeground = ui.Hex("#052505"), theme.Success
	}
	// A capture window can keep focus while the controls still need a recognizable primary action.
	theme.InactiveAccent = theme.Accent
	return palette
}

func (application *applicationState) commandBar(context *ui.Context, compact bool, width float32) {
	appearance := fluentAppearance(context.Theme().Dark)
	ui.Row(context).FillWidth().Shrink(0).Padding(12, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		if !compact {
			ui.Image(context, applicationBranding().logo).Size(40, 40).Fit(ui.Contain).Radius(40 * branding.CornerRadiusRatio).Clip().Label("MyGOFrame logo")
		}
		ui.Column(context).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(context, "MyGOFrame").FontSize(20).FontWeight(600).SingleLine()
			if !compact {
				ui.Text(context, "Capture viewer").FontSize(12).TextColor(appearance.theme.TextMuted)
			}
		})
		if !compact || width >= 560 {
			if ui.Button(context.Key("capture-window"), "Capture window").Tooltip("Open a separate video-only window for screen sharing").Clicked() {
				application.openPresentation()
			}
		}
		if compact {
			if ui.Button(context.Key("settings"), "Settings").Clicked() {
				application.settingsOpen = true
			}
		}
		fullscreenLabel := "Fullscreen · F11"
		if compact {
			fullscreenLabel = "F11"
		}
		if ui.Button(context.Key("fullscreen"), fullscreenLabel).Tooltip("Show the preview fullscreen").Clicked() {
			application.setFullscreen(true)
		}
	})
}

func (application *applicationState) captureStatus(context *ui.Context, appearance *fluentPalette) {
	label := "Ready"
	background, foreground := appearance.theme.SurfaceHover, appearance.theme.TextMuted
	if application.busy {
		label = "Updating"
	} else if application.running {
		label = "Live"
		background, foreground = appearance.successBackground, appearance.successForeground
	}
	ui.Row(context).Padding(4, 8).Gap(6).Radius(4).Background(background).AlignItems(ui.Center).Children(func() {
		ui.Box(context).Size(6, 6).Radius(3).Background(foreground)
		ui.Text(context, label).FontSize(12).FontWeight(600).TextColor(foreground)
	})
}

func (application *applicationState) previewStatus(context *ui.Context, compact bool, height float32, appearance *fluentPalette) {
	maximumHeight := min(float32(120), height/3)
	if compact {
		maximumHeight = min(float32(60), height/4)
	}
	ui.Scroll(context).FillWidth().Shrink(0).MaxHeight(max(28, maximumHeight)).Label("Capture status").Children(func() {
		ui.Column(context).FillWidth().Padding(8, 12).Gap(4).Children(func() {
			if description := actualVideoSummary(application.status); description != "" && !compact {
				ui.Text(context, description).FontSize(14).FontWeight(600)
			}
			ui.Text(context, statusSummary(application.status)).FontSize(12).TextColor(appearance.theme.TextMuted)
			if reason, _ := application.status["fallbackReason"].(string); reason != "" {
				ui.Text(context, "GPU video unavailable; using CPU processing.").FontSize(12).TextColor(appearance.theme.TextMuted)
			}
			if application.notice != "" {
				ui.Text(context, application.notice).FontSize(12).TextColor(appearance.theme.TextMuted)
			}
			if application.errorMessage != "" {
				ui.Text(context, application.errorMessage).FontSize(12).TextColor(appearance.theme.Danger)
			}
		})
	})
}

func (application *applicationState) settingsBody(context *ui.Context) {
	ui.Tabs(context.Key("settings-pages"), &application.settingsPage, "Video", "Audio", "Sharing").FillWidth().Shrink(0).Margin(0, 8).Label("Settings categories")
	ui.Scroll(context).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(context).FillWidth().Padding(16).Gap(16).Children(func() {
			switch application.settingsPage {
			case 1:
				application.audioControls(context)
			case 2:
				application.sharingControls(context)
			default:
				application.videoControls(context)
			}
		})
	})
}

func field(context *ui.Context, label string, control func()) {
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, label).FontSize(12).FontWeight(600)
		control()
	})
}

func helpText(context *ui.Context, text string) {
	ui.Text(context, text).FontSize(12).TextColor(context.Theme().TextMuted)
}

func (application *applicationState) videoControls(context *ui.Context) {
	disabled := application.busy || application.running
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, "Video input").FontSize(16).FontWeight(600)
		helpText(context, "Choose the signal from your capture card.")
	})
	deviceLabels := make([]string, 0, len(application.devices))
	for _, device := range application.devices {
		deviceLabels = append(deviceLabels, videoLabel(device))
	}
	field(context, "Capture device", func() {
		if ui.Select(context.Key("video-device"), &application.device, deviceLabels).FillWidth().Label("Capture device").Disabled(disabled || len(deviceLabels) == 0).Changed() {
			application.loadModes()
		}
	})
	if len(deviceLabels) == 0 {
		helpText(context, "No capture device found. Connect a device and refresh.")
	}
	field(context, "Video format", func() {
		if ui.Select(context.Key("video-format"), &application.selection.format, application.selection.formats()).FillWidth().Label("Video format").Disabled(disabled).Changed() {
			application.selection.reconcile()
		}
	})
	field(context, "Resolution", func() {
		if ui.Select(context.Key("video-resolution"), &application.selection.resolution, application.selection.resolutions()).FillWidth().Label("Resolution").Disabled(disabled).Changed() {
			application.selection.reconcile()
		}
	})
	field(context, "Frame rate", func() {
		ui.Select(context.Key("video-frame-rate"), &application.selection.frameRate, application.selection.frameRates()).FillWidth().Label("Frame rate").Disabled(disabled)
	})
	field(context, "Color range", func() {
		ui.Select(context.Key("video-range"), &application.colorRange, []string{"Auto", "Limited", "Full"}).FillWidth().Label("Color range").Disabled(disabled)
	})
	if application.running {
		if ui.Button(context.Key("stop-video"), "Stop video").FillWidth().Disabled(application.busy).Clicked() {
			application.stopVideo()
		}
		helpText(context, "Stop video to change the capture settings.")
	} else {
		_, valid := application.selection.selected()
		if ui.PrimaryButton(context.Key("start-video"), "Start video").FillWidth().Disabled(application.busy || !valid).Clicked() {
			application.startVideo()
		}
	}
}

func (application *applicationState) audioControls(context *ui.Context) {
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, "Audio monitoring").FontSize(16).FontWeight(600)
		helpText(context, "Listen to your capture card through a speaker or headset.")
	})
	sourceLabels, outputLabels := []string{audioOff}, []string{defaultOutput}
	for _, device := range application.audioDevices.Sources {
		sourceLabels = append(sourceLabels, audioLabel(device))
	}
	for _, device := range application.audioDevices.Outputs {
		outputLabels = append(outputLabels, audioLabel(device))
	}
	field(context, "Capture audio", func() {
		if ui.Select(context.Key("audio-source"), &application.audioSource, sourceLabels).FillWidth().Label("Capture audio").Disabled(application.busy).Changed() {
			application.changeAudio()
		}
	})
	field(context, "Speaker output", func() {
		if ui.Select(context.Key("audio-output"), &application.audioOutput, outputLabels).FillWidth().Label("Speaker output").Disabled(application.busy).Changed() {
			application.changeAudio()
		}
	})
	helpText(context, "Selecting an input starts playback immediately. Choose Off to stop monitoring.")
	ui.Column(context).FillWidth().Gap(8).Children(func() {
		ui.Row(context).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Textf(context, "Volume %.0f%%", application.volume).FontSize(14).FontWeight(600).Grow(1)
			if ui.Checkbox(context.Key("mute"), &application.muted, "Mute").Disabled(application.busy).Changed() {
				application.setVolume()
			}
		})
		if ui.Slider(context.Key("volume"), &application.volume, 0, 100).FillWidth().Label("Volume").Disabled(application.busy).Changed() {
			application.setVolume()
		}
	})
	helpText(context, "Monitoring volume affects this app only. Audio continues when video is stopped.")
}

func (application *applicationState) sharingControls(context *ui.Context) {
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, "Capture window").FontSize(16).FontWeight(600)
		helpText(context, "Share only the video in a separate window.")
	})
	label := "Open capture window"
	if application.presentation != nil {
		label = "Show capture window"
	}
	if ui.PrimaryButton(context.Key("open-capture-window"), label).FillWidth().Clicked() {
		application.openPresentation()
	}
	if application.presentation != nil {
		if ui.Button(context.Key("close-capture-window"), "Close capture window").FillWidth().Clicked() {
			application.closePresentation()
		}
	}
	helpText(context, "In Discord, select the MyGOFrame Capture window as the screen-sharing source. Settings and status stay in this window.")
	helpText(context, "The complete video keeps its aspect ratio when the capture window is resized. F11 toggles fullscreen.")
}
