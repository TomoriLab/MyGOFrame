package presentation

import (
	"math"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/domain/branding"

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
			ui.Text(context, "MyGOFrame").FillWidth().FontSize(20).FontWeight(600).SingleLine()
			if !compact {
				ui.Text(context, "Capture viewer").FontSize(12).TextColor(appearance.theme.TextMuted)
			}
		})
		if !compact && width >= 1120 && len(application.presets) > 0 {
			application.quickPresetControls(context, 200, true, 8, false)
		}
		if compact {
			if ui.Button(context.Key("settings"), "Settings").Clicked() {
				application.settingsOpen = true
			}
		}
		ui.Row(context).Shrink(0).Gap(4).AlignItems(ui.Center).Label("Window actions").Children(func() {
			if compact {
				button := ui.ButtonBase(context.Key("capture-window")).Size(28, 32).Label("Capture window").Tooltip("Open the video-only capture window")
				button.Draw(func(painter *ui.Painter, bounds ui.Rect) { drawSettingsIcon(painter, bounds, 0, appearance.theme.Text) })
				if button.Clicked() {
					application.openPresentation()
				}
			} else if ui.Button(context.Key("capture-window"), "Capture window").Label("Open capture window action").Tooltip("Open a separate video-only window for screen sharing").Clicked() {
				application.openPresentation()
			}
			fullscreenLabel := "Fullscreen · F11"
			if compact {
				fullscreenLabel = "F11"
			}
			if ui.Button(context.Key("fullscreen"), fullscreenLabel).Label("Fullscreen action").Tooltip("Show the preview fullscreen").Clicked() {
				application.setFullscreen(true)
			}
		})
		if len(application.presets) == 0 {
			application.recordingCommandButton(context, compact)
		}
	})
}

func (application *applicationState) recordingCommandButton(context *ui.Context, compact bool) {
	appearance := fluentAppearance(context.Theme().Dark)
	label := "Record"
	if application.status.Recording.Active() {
		label = "Stop recording"
	}
	disabled := application.busy || application.recordingProbeBusy || application.status.Recording.Phase == "stopping" || (!application.running && !application.status.Recording.Active())
	var button ui.Element
	if compact {
		button = ui.ButtonBase(context.Key("record-toggle")).Size(28, 32).Label(label).Tooltip(label).Disabled(disabled)
		button.Draw(func(painter *ui.Painter, bounds ui.Rect) {
			color := appearance.theme.Danger
			if disabled {
				color = appearance.theme.TextMuted
			}
			radius := float32(6)
			if application.status.Recording.Active() {
				radius = 2
			}
			painter.Fill(ui.Rect{X: bounds.X + (bounds.W-12)/2, Y: bounds.Y + (bounds.H-12)/2, W: 12, H: 12}, color, radius)
		})
	} else {
		button = ui.Button(context.Key("record-toggle"), label).Size(128, 32).Label("Recording action").Disabled(disabled)
	}
	if button.Clicked() {
		if application.status.Recording.Active() {
			application.stopRecording()
		} else {
			application.startRecording()
		}
	}
}

func (application *applicationState) themeSwitch(context *ui.Context) {
	palette := fluentAppearance(context.Theme().Dark)
	tooltip := "Use dark theme"
	if application.darkTheme {
		tooltip = "Use light theme"
	}
	button := ui.ToggleBase(context.Key("theme-switch"), &application.darkTheme).Size(32, 32).Shrink(0).Radius(6).Label("Theme switch").Tooltip(tooltip)
	background := palette.card
	if button.Hovered() {
		background = palette.theme.SurfaceHover
	}
	button.Background(background).Draw(func(painter *ui.Painter, bounds ui.Rect) {
		centerX, centerY := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
		color := palette.theme.Text
		if application.darkTheme {
			painter.Fill(ui.Rect{X: centerX - 8, Y: centerY - 8, W: 16, H: 16}, color, 8)
			painter.Fill(ui.Rect{X: centerX - 2, Y: centerY - 11, W: 15, H: 15}, background, 7.5)
		} else {
			painter.Stroke(ui.Rect{X: centerX - 4.5, Y: centerY - 4.5, W: 9, H: 9}, color, 4.5, 1.5)
			for index := 0; index < 8; index++ {
				angle := float64(index) * math.Pi / 4
				horizontal, vertical := float32(math.Cos(angle)), float32(math.Sin(angle))
				painter.Line(centerX+horizontal*7, centerY+vertical*7, centerX+horizontal*10, centerY+vertical*10, 1.5, color)
			}
		}
	})
	if button.Changed() {
		application.themeOverride = true
	}
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
			summary := statusSummary(application.status)
			if application.status.Recording.Active() || application.status.Recording.Phase == "failed" {
				summary = recordingSummary(application.status.Recording)
			}
			ui.Text(context, summary).FontSize(12).TextColor(appearance.theme.TextMuted)
			if application.status.FallbackReason != "" {
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

var settingsCategories = []struct{ title, description string }{
	{"Video", "Capture device, format, and picture"},
	{"Audio", "Capture sound and speaker monitoring"},
	{"Recording", "GPU, codec, and recording quality"},
	{"Presets", "Save and restore your settings"},
	{"About", "App, developer, and open-source licenses"},
}

func (application *applicationState) settingsBody(context *ui.Context) {
	if application.settingsPage < 0 || application.settingsPage >= len(settingsCategories) {
		application.settingsOverview(context)
		return
	}
	ui.Row(context).FillWidth().Shrink(0).Padding(0, 16, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		back := ui.ButtonBase(context.Key("settings-back")).Size(28, 32).Shrink(0).Radius(4).Label("Back to settings").Tooltip("Back to settings")
		back.Justify(ui.Center).AlignItems(ui.Center).Children(func() { ui.Text(context, "‹").FontSize(24).TextColor(context.Theme().Text) })
		if back.Clicked() {
			application.settingsPage = -1
		}
		ui.Text(context, settingsCategories[max(0, application.settingsPage)].title).FontSize(16).FontWeight(600)
	})
	if application.settingsPage < 0 {
		return
	}
	ui.Scroll(context).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(context).FillWidth().Padding(8, 16, 16).Gap(16).Children(func() {
			switch application.settingsPage {
			case 1:
				application.audioControls(context)
			case 2:
				application.recordingControls(context)
			case 3:
				application.presetControls(context)
			default:
				application.videoControls(context)
			}
		})
	})
}

func (application *applicationState) settingsOverview(context *ui.Context) {
	palette := fluentAppearance(context.Theme().Dark)
	iconColor := palette.theme.Accent
	if context.Theme().Dark {
		iconColor = ui.Hex("#479ef5")
	}
	ui.Scroll(context.Key("settings-overview")).FillWidth().Grow(1).MinHeight(0).Label("Settings overview").Children(func() {
		ui.Column(context).FillWidth().Padding(0, 12, 12).Gap(8).Children(func() {
			for index, category := range settingsCategories {
				button := ui.ButtonBase(context.Key(category.title)).FillWidth().MinWidth(0).Shrink(0).Height(76).Padding(12).Gap(12).AlignItems(ui.Center).Background(palette.card).Border(1, palette.stroke).Radius(6).Label(category.title + " settings")
				if button.Hovered() {
					button.Background(palette.theme.SurfaceHover)
				}
				if button.Clicked() {
					if category.title == "About" {
						application.openAbout()
					} else {
						application.settingsPage = index
					}
				}
				button.Children(func() {
					ui.Box(context).Size(28, 28).Shrink(0).Draw(func(painter *ui.Painter, bounds ui.Rect) {
						drawSettingsIcon(painter, bounds, index, iconColor)
					})
					ui.Column(context).Grow(1).MinWidth(0).Gap(4).Children(func() {
						ui.Text(context, category.title).FontSize(14).FontWeight(600)
						ui.Text(context, category.description).FillWidth().FontSize(12).TextColor(palette.theme.TextMuted)
					})
					ui.Text(context, "›").FontSize(20).TextColor(palette.theme.TextMuted).Width(12).Shrink(0).TextAlign(ui.Center)
				})
			}
		})
	})
}

func drawSettingsIcon(painter *ui.Painter, bounds ui.Rect, index int, color ui.Color) {
	x, y := bounds.X+4, bounds.Y+4
	switch index {
	case 0:
		painter.Stroke(ui.Rect{X: x, Y: y + 2, W: 20, H: 14}, color, 2, 1.5)
		painter.Line(x+10, y+16, x+10, y+20, 1.5, color)
		painter.Line(x+5, y+20, x+15, y+20, 1.5, color)
	case 1:
		painter.Stroke(ui.Rect{X: x + 6, Y: y, W: 8, H: 13}, color, 4, 1.5)
		painter.Line(x+3, y+9, x+3, y+14, 1.5, color)
		painter.Line(x+17, y+9, x+17, y+14, 1.5, color)
		painter.Line(x+3, y+14, x+10, y+18, 1.5, color)
		painter.Line(x+17, y+14, x+10, y+18, 1.5, color)
		painter.Line(x+10, y+18, x+10, y+22, 1.5, color)
	case 3:
		for offset := float32(0); offset <= 12; offset += 6 {
			painter.Line(x, y+offset+3, x+20, y+offset+3, 1.5, color)
			painter.Fill(ui.Rect{X: x + 5 + offset/2, Y: y + offset, W: 3, H: 6}, color, 1)
		}
	case 4:
		painter.Stroke(ui.Rect{X: x, Y: y, W: 20, H: 20}, color, 10, 1.5)
		painter.Fill(ui.Rect{X: x + 9, Y: y + 4, W: 2, H: 2}, color, 1)
		painter.Line(x+10, y+9, x+10, y+16, 1.5, color)
	case 2:
		painter.Stroke(ui.Rect{X: x, Y: y, W: 20, H: 20}, color, 10, 1.5)
		painter.Fill(ui.Rect{X: x + 6, Y: y + 6, W: 8, H: 8}, color, 4)
	}
}

func field(context *ui.Context, label string, control func()) {
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, label).FontSize(12).FontWeight(600)
		control()
	})
}

func helpText(context *ui.Context, text string) {
	ui.Text(context, text).FillWidth().MinWidth(0).FontSize(12).TextColor(context.Theme().TextMuted)
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
		if ui.Select(context.Key("audio-source"), &application.audioSource, sourceLabels).FillWidth().Label("Capture audio").Disabled(application.busy || application.status.Recording.Active()).Changed() {
			application.changeAudio()
		}
	})
	field(context, "Speaker output", func() {
		if ui.Select(context.Key("audio-output"), &application.audioOutput, outputLabels).FillWidth().Label("Speaker output").Disabled(application.busy).Changed() {
			application.changeAudio()
		}
	})
	ui.Textf(context, "Input level %.0f%%", application.status.Audio.InputPeak*100).FontSize(12)
	if ui.Checkbox(context.Key("monitor-audio"), &application.monitorAudio, "Monitor audio").Disabled(application.busy).Changed() {
		application.changeMonitoring()
	}
	helpText(context, "The capture-card input is also used for recording. Monitor volume and mute do not affect the recorded level.")
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
