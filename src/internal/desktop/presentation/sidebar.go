package presentation

import "github.com/egoist/mygo/ui"

func (application *applicationState) sidebarActions(context *ui.Context) {
	palette := fluentAppearance(context.Theme().Dark)
	ui.Column(context).FillWidth().Shrink(0).Padding(12).Gap(8).Background(palette.card).Children(func() {
		if len(application.presets) > 0 {
			names := make([]string, 0, len(application.presets))
			for _, preset := range application.presets {
				names = append(names, preset.Name)
			}
			previous := application.selectedPreset
			if ui.Select(context.Key("sidebar-preset"), &application.selectedPreset, names).FillWidth().Label(text(context, "Capture preset")).Disabled(application.busy || application.presetBusy || application.status.Recording.Active()).Changed() {
				application.activateSelectedPreset(false, previous)
			}
		}
		ui.Row(context).FillWidth().Height(32).Shrink(0).Gap(8).Children(func() {
			ui.Box(context).Width(0).Grow(1).MinWidth(0).Height(32).Children(func() { application.recordingCommandButton(context, false).FillWidth().Height(32) })
			ui.Box(context).Width(0).Grow(1).MinWidth(0).Height(32).Children(func() {
				label := "Start video"
				if application.running {
					label = "Stop video"
				}
				_, valid := application.selection.selected()
				if ui.PrimaryButton(context.Key("sidebar-capture"), text(context, label)).Label(text(context, "Preset video action")).FillWidth().MinWidth(0).Height(32).Disabled(application.busy || application.presetBusy || (!application.running && !valid && application.selectedPreset == "")).Clicked() {
					if application.running {
						application.stopVideo()
					} else if application.selectedPreset != "" {
						application.activateSelectedPreset(true, application.selectedPreset)
					} else {
						application.startVideo()
					}
				}
			})
		})
		ui.Button(context.Key("sidebar-window"), text(context, "Capture window")).FillWidth().Height(32).Shrink(0).OnClick(func() { application.openPresentation() })
		if ui.Button(context.Key("sidebar-fullscreen"), text(context, "Fullscreen · F11")).FillWidth().Height(32).Shrink(0).Clicked() {
			application.setFullscreen(true)
		}
	})
}

// The panel outline identifies sidebar navigation independently of the settings gear.
func drawSidebarToggle(painter *ui.Painter, bounds ui.Rect, collapsed bool, color ui.Color) {
	x, y := bounds.X+(bounds.W-22)/2, bounds.Y+(bounds.H-18)/2
	painter.Stroke(ui.Rect{X: x, Y: y, W: 22, H: 18}, color, 3, 1.5)
	painter.Line(x+7, y+1, x+7, y+17, 1.5, color)
	centerX, centerY := x+14, y+9
	direction := float32(-1)
	if collapsed {
		direction = 1
	}
	painter.Line(centerX-direction*2, centerY-3, centerX+direction*2, centerY, 1.5, color)
	painter.Line(centerX+direction*2, centerY, centerX-direction*2, centerY+3, 1.5, color)
}
