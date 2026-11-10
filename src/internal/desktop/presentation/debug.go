package presentation

import (
	"encoding/json"

	diagnostics "github.com/TomoriLab/MyGOFrame/src/internal/diagnostics/domain"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"
	version "github.com/TomoriLab/MyGOFrame/src/internal/version/domain"
	"github.com/egoist/mygo/ui"
)

type diagnosticReport struct {
	Build       version.Information     `json:"build"`
	Session     session.SessionSnapshot `json:"session"`
	Diagnostics diagnostics.Snapshot    `json:"diagnostics"`
}

func (application *applicationState) debugEvent(name, detail string) {
	if recorder := application.services.Diagnostics; recorder != nil {
		recorder.Record(name, detail)
	}
}

func (application *applicationState) setDebugMode(enabled bool) {
	recorder := application.services.Diagnostics
	if recorder == nil || application.debugUpdating || application.closing {
		return
	}
	application.debugUpdating = true
	done := make(chan struct{})
	application.debugDone = done
	apply := func(err error) {
		application.debugUpdating = false
		application.debugEnabled = recorder.Enabled()
		if err != nil {
			application.errorMessage = err.Error()
		}
	}
	if application.window == nil {
		// Headless controls use an injected recorder without scheduling a native window callback.
		apply(recorder.SetEnabled(enabled))
		close(done)
		return
	}
	go func() {
		defer close(done)
		err := recorder.SetEnabled(enabled)
		application.window.Update(func() {
			if !application.closing {
				apply(err)
			}
		})
	}()
}

func (application *applicationState) debugControls(context *ui.Context) {
	recorder := application.services.Diagnostics
	ui.Divider(context).FillWidth()
	ui.Row(context).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(context, text(context, "Debug mode")).Grow(1).MinWidth(0).FontSize(16).FontWeight(600)
		if ui.Switch(context.Key("debug-mode"), &application.debugEnabled).Label(text(context, "Debug mode switch")).Disabled(recorder == nil || application.debugUpdating).Changed() {
			application.setDebugMode(application.debugEnabled)
		}
	})
	helpText(context, "Keep a bounded diagnostic history for this session. No per-frame logging or automatic uploads.")
	var state diagnostics.State
	if recorder != nil {
		state = recorder.State()
	}
	helpText(context, message(context, "{count, plural, one {# event retained} other {# events retained}} · {dropped} discarded", map[string]any{"count": state.EventCount, "dropped": state.Dropped}))
	if state.Error != "" {
		ui.Text(context, state.Error).FillWidth().FontSize(12).TextColor(context.Theme().Danger)
	}
	if ui.Button(context.Key("copy-diagnostics"), text(context, "Copy diagnostic report")).FillWidth().Disabled(recorder == nil).Clicked() {
		data, err := json.MarshalIndent(diagnosticReport{Build: application.services.BuildInformation, Session: application.status, Diagnostics: recorder.Snapshot()}, "", "  ")
		if err != nil {
			application.errorMessage = err.Error()
		} else {
			context.WriteClipboard(string(data))
		}
	}
	if ui.Button(context.Key("clear-diagnostics"), text(context, "Clear diagnostic history")).FillWidth().Disabled(recorder == nil || state.EventCount == 0).Clicked() {
		recorder.Clear()
	}
}
