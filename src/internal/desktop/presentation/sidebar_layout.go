package presentation

import "github.com/egoist/mygo/ui"

func (application *applicationState) sidebarLayout(context *ui.Context, compact bool, height float32) {
	appearance := fluentAppearance(context.Theme().Dark)
	collapsed := application.sidebarCollapsed || compact
	width := float32(330)
	if collapsed {
		width = 40
	}
	ui.Column(context).Width(width).Shrink(0).FillHeight().Gap(0).Background(appearance.card).Label(text(context, "Capture settings")).Children(func() {
		ui.Row(context).FillWidth().Height(48).Shrink(0).AlignItems(ui.Center).Children(func() {
			toggle := ui.ButtonBase(context.Key("sidebar-toggle")).Size(40, 40).Shrink(0).Label(text(context, "Toggle sidebar")).Tooltip(text(context, "Toggle sidebar"))
			toggle.Draw(func(painter *ui.Painter, bounds ui.Rect) {
				drawSidebarToggle(painter, bounds, collapsed, context.Theme().Text)
			})
			if toggle.Clicked() {
				if compact {
					application.settingsOpen = true
				} else {
					application.sidebarCollapsed = !application.sidebarCollapsed
				}
			}
			if !collapsed {
				ui.Text(context, "MyGOFrame").FontSize(20).FontWeight(600).Grow(1).MinWidth(0).SingleLine()
				application.themeSwitch(context)
				ui.Box(context).Width(12).Shrink(0)
			}
		})
		if collapsed {
			return
		}
		settings := ui.Column(context).FillWidth().MinHeight(0)
		if !application.settingsSectionClosed {
			settings.Grow(1)
		} else {
			settings.Shrink(0)
		}
		settings.Children(func() {
			ui.Row(context).FillWidth().Height(44).Shrink(0).AlignItems(ui.Center).Children(func() {
				sectionToggle(context, "Settings", &application.settingsSectionClosed).Grow(1)
			})
			if !application.settingsSectionClosed {
				application.settingsBody(context)
			}
		})
		ui.Column(context).FillWidth().Shrink(0).Children(func() {
			sectionToggle(context, "Capture controls", &application.controlsSectionClosed).FillWidth()
			if !application.controlsSectionClosed {
				application.sidebarActions(context)
			}
		})
		ui.Column(context).FillWidth().Shrink(0).Children(func() {
			sectionToggle(context, "Capture information", &application.informationSectionClosed).FillWidth()
			if !application.informationSectionClosed {
				application.previewStatus(context, false, height, appearance)
			}
		})
	})
}

func sectionToggle(context *ui.Context, title string, closed *bool) ui.Element {
	button := ui.ButtonBase(context.Key(title + "-section")).Height(44).Shrink(0).Padding(12).Label(text(context, title))
	button.Row().Justify(ui.Start).Gap(8).AlignItems(ui.Center).Children(func() {
		arrow := "⌄"
		if *closed {
			arrow = "›"
		}
		ui.Text(context, arrow).Width(16).TextAlign(ui.Center)
		ui.Text(context, text(context, title)).FontSize(16).FontWeight(600).MinWidth(0).SingleLine()
	})
	if button.Clicked() {
		*closed = !*closed
	}
	return button
}
