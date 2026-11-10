package presentation

import (
	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var supportedLanguages = localization.Languages()

func text(context *ui.Context, message string) string {
	return localization.Text(context.Locale(), message)
}

func message(context *ui.Context, pattern string, arguments map[string]any) string {
	return localization.Format(context.Locale(), pattern, arguments)
}

func messageLocale(contexts []*ui.Context) string {
	if len(contexts) > 0 {
		return contexts[0].Locale()
	}
	return "en"
}

func presetLabelWidth(context *ui.Context) float32 {
	width, _ := context.MeasureText(0, ui.Span{Text: text(context, "Preset"), Size: 12})
	return max(40, width+2)
}

// The native selector edits a display label while presets retain their canonical values.
func localizedSelect(context *ui.Context, selected *string, options []string) ui.Element {
	labels := make([]string, len(options))
	for index, option := range options {
		labels[index] = text(context, option)
	}
	label := text(context, *selected)
	control := ui.Select(context, &label, labels)
	if control.Changed() {
		for index, candidate := range labels {
			if label == candidate {
				*selected = options[index]
				break
			}
		}
	}
	return control
}

func (application *applicationState) appSettingsControls(context *ui.Context) {
	ui.Column(context).FillWidth().Gap(4).Children(func() {
		ui.Text(context, text(context, "Language")).FontSize(16).FontWeight(600)
		helpText(context, "Choose the language used in both windows.")
	})
	application.languageControl(context)
	application.debugControls(context)
}

func (application *applicationState) languageControl(context *ui.Context) {
	options := []string{text(context, "System language")}
	selected := ""
	for _, language := range supportedLanguages {
		options = append(options, language.Name)
		if language.Tag == context.Locale() {
			selected = language.Name
		}
	}
	if ui.Select(context.Key("language-selection"), &selected, options).FillWidth().Label(text(context, "App language")).Changed() {
		tag := ""
		for _, language := range supportedLanguages {
			if selected == language.Name {
				tag = language.Tag
				break
			}
		}
		if application.changeLocale != nil {
			application.changeLocale(tag)
		} else {
			mygo.App.SetLocale(tag)
		}
	}
}
