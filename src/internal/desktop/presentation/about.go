package presentation

import (
	"strings"

	about "github.com/TomoriLab/MyGOFrame/src/internal/about/domain"

	"github.com/egoist/mygo/ui"
)

func (application *applicationState) openAbout() {
	application.aboutOpen = true
	if application.aboutCatalog == nil {
		application.aboutCatalog, application.aboutError = application.services.Notices()
	}
}

func (application *applicationState) showComponentLicense(component about.Component) {
	if application.aboutCatalog == nil {
		return
	}
	application.licenseComponent = component
	application.licenseBody = application.aboutCatalog.LicenseText(component)
	application.licenseNeedsReset = true
	application.licenseOpen = true
}

func modalSize(width, height, maximumWidth, maximumHeight float32) (float32, float32) {
	return min(maximumWidth, max(1, width-24)), min(maximumHeight, max(1, height-24))
}

func (application *applicationState) aboutDialogs(context *ui.Context, width, height float32) {
	palette := fluentAppearance(context.Theme().Dark)
	ui.DialogBase(context.Key("about-modal"), &application.aboutOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .5))
		panelWidth, panelHeight := modalSize(width, height, 760, 720)
		panel.Column().Size(panelWidth, panelHeight).Background(palette.card).Border(1, palette.stroke).Radius(12).Label(text(context, "About dialog")).Children(func() {
			modalHeader(context, "About MyGOFrame", "Close app information", func() { application.aboutOpen = false })
			ui.Scroll(context.Key("about-scroll")).FillWidth().Grow(1).MinHeight(0).Label(text(context, "About information")).Children(func() {
				ui.Column(context).FillWidth().Padding(16).Gap(16).Children(func() {
					application.aboutContents(context)
					if panelHeight < 560 {
						application.aboutLicenseBlock(context)
					}
				})
			})
			if panelHeight >= 560 {
				ui.Column(context).FillWidth().Shrink(0).Padding(16).Children(func() { application.aboutLicenseBlock(context) })
			}
		})
	})
	ui.DialogBase(context.Key("notices-modal"), &application.noticesOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .5))
		panelWidth, panelHeight := modalSize(width, height, 760, 720)
		panel.Column().Size(panelWidth, panelHeight).Background(palette.card).Border(1, palette.stroke).Radius(12).Label(text(context, "Open source and runtime notices")).Children(func() {
			modalHeader(context, "Open source and runtime notices", "Close", func() { application.noticesOpen = false })
			ui.Scroll(context.Key("notices-scroll")).FillWidth().Grow(1).MinHeight(0).Label(text(context, "Search licenses")).Children(func() {
				ui.Column(context).FillWidth().Padding(16).Gap(16).Children(func() { application.noticeContents(context) })
			})
		})
	})
	ui.DialogBase(context.Key("license-modal"), &application.licenseOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, .5))
		panelWidth, panelHeight := modalSize(width, height, 860, 760)
		panel.Column().Size(panelWidth, panelHeight).Background(palette.card).Border(1, palette.stroke).Radius(12).Label(text(context, "License dialog")).Children(func() {
			modalHeader(context, message(context, "{name} license", map[string]any{"name": application.licenseComponent.Name}), "Close license", func() { application.licenseOpen = false })
			ui.Row(context).FillWidth().Shrink(0).Padding(0, 16, 8).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(context, text(context, application.licenseComponent.License)).Grow(1).MinWidth(0).FontSize(12).SingleLine().Tooltip(text(context, application.licenseComponent.License)).TextColor(palette.theme.TextMuted)
				if application.licenseComponent.URL != "" {
					ui.Link(context, text(context, "Upstream project"), application.licenseComponent.URL).Shrink(0).FontSize(12).TextColor(aboutLinkColor(context.Theme().Dark))
				}
			})
			ui.Column(context).FillWidth().Grow(1).MinHeight(0).Padding(0, 16).Children(func() {
				reader := ui.TextArea(context.Key(application.licenseComponent.ID), &application.licenseBody).FillWidth().Grow(1).MinWidth(0).MinHeight(0).ReadOnly(true).Font("monospace").FontSize(12).Label(text(context, "License text"))
				if application.licenseNeedsReset {
					// A notice starts at its first line instead of the text editor's default end position.
					reader.SetTextSelection(0, 0)
					application.licenseNeedsReset = false
				}
			})
			ui.Row(context).FillWidth().Shrink(0).Padding(8, 16).Justify(ui.End).Children(func() {
				if ui.Button(context.Key("copy-license"), text(context, "Copy license text")).Clicked() {
					context.WriteClipboard(application.licenseBody)
				}
			})
		})
	})
	if !application.aboutOpen {
		application.noticesOpen = false
		application.licenseOpen = false
	}
}

func aboutLinkColor(dark bool) ui.Color {
	if dark {
		return ui.Hex("#75b6f5")
	}
	return fluentLight.theme.Accent
}

func modalHeader(context *ui.Context, title, closeLabel string, closeAction func()) {
	ui.Row(context).FillWidth().Shrink(0).Padding(12, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(context, text(context, title)).Grow(1).MinWidth(0).FontSize(18).FontWeight(600).SingleLine()
		if ui.Button(context.Key(closeLabel), text(context, "Close")).Shrink(0).Label(text(context, closeLabel)).Clicked() {
			closeAction()
		}
	})
}

func (application *applicationState) aboutContents(context *ui.Context) {
	if application.aboutError != nil {
		ui.Text(context, text(context, application.aboutError.Error())).FillWidth().TextColor(context.Theme().Danger)
		return
	}
	catalog := application.aboutCatalog
	if catalog == nil {
		return
	}
	ui.Row(context).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Image(context, applicationBranding().logo).Size(96, 96).Shrink(0).Fit(ui.Contain).Radius(20).Clip().Label(text(context, "MyGOFrame logo"))
		ui.Column(context).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(context, "MyGOFrame").FontSize(22).FontWeight(600)
			ui.Text(context, text(context, message(context, "Version {version}", map[string]any{"version": catalog.Version}))).FillWidth().FontSize(13)
			if catalog.DebianVersion != "" {
				helpText(context, message(context, "Package version {version}", map[string]any{"version": catalog.DebianVersion}))
			}
			if catalog.Revision != "" {
				modified := ""
				if catalog.Modified {
					modified = text(context, " (modified)")
				}
				sourceRevision := catalog.Revision
				if len(sourceRevision) > 12 {
					sourceRevision = sourceRevision[:12]
				}
				revision := message(context, "Build {revision}{modified}", map[string]any{"revision": sourceRevision, "modified": modified})
				helpText(context, revision)
			}
		})
	})
	helpText(context, "Low-latency capture-card video, audio monitoring, and recording.")
	ui.Column(context).FillWidth().Gap(6).Children(func() {
		ui.Text(context, text(context, "Developer")).FontSize(16).FontWeight(600)
		ui.Text(context, text(context, catalog.Developer+" / "+catalog.Organization)).FillWidth().FontSize(14).Selectable()
		ui.Link(context, text(context, catalog.Contact), "mailto:"+catalog.Contact).FontSize(13).TextColor(aboutLinkColor(context.Theme().Dark))
		ui.Link(context, text(context, "Project and source code"), catalog.ProjectURL).FontSize(13).TextColor(aboutLinkColor(context.Theme().Dark))
	})
}

func (application *applicationState) aboutLicenseBlock(context *ui.Context) {
	catalog := application.aboutCatalog
	if catalog == nil {
		return
	}
	ui.Column(context).FillWidth().Padding(16).Gap(12).Background(context.Theme().Surface).Border(1, context.Theme().Border).Radius(8).Children(func() {
		ui.Text(context, text(context, "License and acknowledgments")).FillWidth().FontSize(16).FontWeight(600)
		ui.Text(context, "MIT License · Copyright © 2026 "+catalog.Developer).FillWidth().FontSize(14)
		helpText(context, "Third-party components retain their respective licenses.")
		buttons := ui.Column(context).FillWidth().Gap(8)
		width, _ := context.Size()
		if width >= 700 {
			buttons.Row()
		}
		buttons.Children(func() {
			if ui.Button(context.Key("application-license"), text(context, "View license")).Label(text(context, "View MyGOFrame license")).Clicked() {
				application.showComponentLicense(catalog.Components[0])
			}
			if ui.Button(context.Key("open-notices"), text(context, "Dependencies and licenses")).Label(text(context, "Open source and runtime notices")).Clicked() {
				application.noticesOpen = true
			}
		})
	})
}

func (application *applicationState) noticeContents(context *ui.Context) {
	catalog := application.aboutCatalog
	if catalog == nil {
		return
	}
	helpText(context, message(context, "{count, plural, one {# component.} other {# components.}} Bundled and system-linked components retain their own terms.", map[string]any{"count": len(catalog.Components) - 1}))
	ui.TextInput(context.Key("license-search"), &application.licenseSearch).FillWidth().Placeholder(text(context, "Search component or license")).Label(text(context, "Search licenses"))
	query := strings.ToLower(strings.TrimSpace(application.licenseSearch))
	matches := 0
	for _, component := range catalog.Components {
		if component.ID == "mygoframe" {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(component.Name+" "+component.License+" "+component.Scope+" "+text(context, component.Scope)), query) {
			continue
		}
		matches++
		entry := component
		button := ui.ButtonBase(context.Key(entry.ID)).FillWidth().MinWidth(0).Shrink(0).Padding(12).Background(context.Theme().SurfaceHover).Border(1, context.Theme().Border).Radius(6).Label(message(context, "License for {name}", map[string]any{"name": entry.Name}))
		if button.Clicked() {
			application.showComponentLicense(entry)
		}
		button.Children(func() {
			ui.Column(context).FillWidth().MinWidth(0).Gap(4).Children(func() {
				ui.Text(context, text(context, entry.Name)).FillWidth().MinWidth(0).FontSize(14).FontWeight(600)
				helpText(context, entry.Version+" · "+text(context, entry.Scope))
				helpText(context, entry.License)
				ui.Text(context, text(context, "View license ›")).FontSize(12).TextColor(aboutLinkColor(context.Theme().Dark))
			})
		})
	}
	if matches == 0 {
		helpText(context, "No matching components.")
	}
}
