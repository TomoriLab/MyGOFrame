package presentation

import (
	"fmt"
	"strings"

	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation/localization"
	"github.com/egoist/mygo/ui"
)

func recordingDeviceLabel(device session.RecordingDevice) string {
	return fmt.Sprintf("%s · %s (%s)", device.Name, strings.ToUpper(device.Backend), device.PCI)
}
func (application *applicationState) refreshRecordingDevices() {
	if application.recordingProbeBusy || application.window == nil || application.closing || application.status.Recording.Active() {
		return
	}
	application.recordingProbeBusy = true
	done := make(chan struct{})
	application.recordingProbeDone = done
	go func() {
		defer close(done)
		devices, err := application.services.RecordingDevices()
		application.window.Update(func() {
			if application.closing {
				return
			}
			application.recordingProbeBusy = false
			if err != nil {
				application.errorMessage = err.Error()
				application.debugEvent("recording.gpu-probe-failed", err.Error())
				return
			}
			application.debugEvent("recording.gpu-probe-completed", fmt.Sprintf("%d devices", len(devices)))
			application.recordingDevices = devices
			application.recordingGPU = "Automatic"
			for _, device := range devices {
				if device.ID == application.recordingConfig.GPUID {
					application.recordingGPU = recordingDeviceLabel(device)
				}
			}
			if application.recordingConfig.GPUID != "auto" && application.recordingGPU == "Automatic" {
				application.recordingGPU = "Unavailable saved GPU"
			}
		})
	}()
}
func (application *applicationState) startRecording() {
	if application.status.Recording.Active() || !application.running || application.recordingProbeBusy {
		return
	}
	config := application.recordingConfig
	application.submit("Starting recording…", func(worker *captureWorker) (func(*applicationState), error) {
		return nil, worker.engine.StartRecording(config)
	})
}
func (application *applicationState) stopRecording() {
	application.submit("Finalizing recording…", func(worker *captureWorker) (func(*applicationState), error) {
		return nil, worker.engine.StopRecording()
	})
}
func (application *applicationState) changeMonitoring() {
	enabled := application.monitorAudio
	application.submit("Updating audio monitor…", func(worker *captureWorker) (func(*applicationState), error) {
		return nil, worker.engine.SetMonitoring(enabled)
	})
}
func recordingSummary(status session.RecordingSnapshot, contexts ...*ui.Context) string {
	if status.Error != "" {
		return localization.Format(messageLocale(contexts), "Recording failed: {error}", map[string]any{"error": status.Error})
	}
	locale := messageLocale(contexts)
	return localization.Format(locale, "Recording {phase} · {seconds} s · {size} MiB · {frames, plural, one {# frame} other {# frames}} · {dropped} dropped", map[string]any{
		"phase": localization.Text(locale, status.Phase), "seconds": fmt.Sprintf("%.1f", float64(status.DurationNanoseconds)/1e9), "size": fmt.Sprintf("%.1f", float64(status.Bytes)/(1024*1024)), "frames": status.Frames, "dropped": status.DroppedFrames,
	})
}
func (application *applicationState) recordingControls(context *ui.Context) {
	active := application.status.Recording.Active()
	disabled := active || application.busy || application.recordingProbeBusy
	ui.Text(context, text(context, "Video recording")).FontSize(16).FontWeight(600)
	helpText(context, "Record the capture card while the preview and sharing window remain active.")
	labels := []string{"Automatic"}
	for _, device := range application.recordingDevices {
		labels = append(labels, recordingDeviceLabel(device))
	}
	field(context, "Recording GPU", func() {
		if localizedSelect(context.Key("recording-gpu"), &application.recordingGPU, labels).FillWidth().Label(text(context, "Recording GPU selection")).Disabled(disabled).Changed() {
			application.recordingConfig.GPUID = "auto"
			for _, device := range application.recordingDevices {
				if recordingDeviceLabel(device) == application.recordingGPU {
					application.recordingConfig.GPUID = device.ID
				}
			}
		}
	})
	if application.recordingProbeBusy {
		helpText(context, "Checking hardware encoder support…")
	}
	codecs := []string{}
	for _, device := range application.recordingDevices {
		if application.recordingConfig.GPUID == "auto" || device.ID == application.recordingConfig.GPUID {
			for _, codec := range device.Codecs {
				found := false
				for _, existing := range codecs {
					if existing == codec {
						found = true
					}
				}
				if !found {
					codecs = append(codecs, codec)
				}
			}
			if device.Error != "" {
				helpText(context, device.Error)
			}
		}
	}
	field(context, "Video codec", func() {
		ui.Select(context.Key("recording-codec"), &application.recordingConfig.Codec, codecs).FillWidth().Label(text(context, "Recording codec")).Disabled(disabled)
	})
	field(context, "Target bitrate (Mbps)", func() {
		ui.NumberInput(context.Key("recording-bitrate"), &application.recordingConfig.BitrateMbps, .1, 1000, .1).FillWidth().Label(text(context, "Recording bitrate")).Disabled(disabled)
	})
	field(context, "Rate control", func() {
		localizedSelect(context.Key("recording-rate"), &application.recordingConfig.RateControl, []string{"vbr", "cbr"}).FillWidth().Label(text(context, "Recording rate control")).Disabled(disabled)
	})
	field(context, "Output file", func() {
		ui.TextInput(context.Key("recording-path"), &application.recordingConfig.Path).FillWidth().Placeholder(text(context, "Automatic: ~/Videos/MyGOFrame/*.mkv")).Label(text(context, "Recording output path")).Disabled(disabled)
	})
	helpText(context, "Use an absolute .mkv or .mp4 path. Existing files are never overwritten. Bitrate is a target and depends on the content.")
	ui.Checkbox(context.Key("recording-audio"), &application.recordingConfig.RecordAudio, text(context, "Record capture audio")).Disabled(disabled)
	helpText(context, "Select the capture-card microphone input in Audio. Monitor volume and mute are independent of recording.")
	if active {
		if ui.PrimaryButton(context.Key("stop-recording"), text(context, "Stop recording")).FillWidth().Disabled(application.busy || application.status.Recording.Phase == "stopping").Clicked() {
			application.stopRecording()
		}
	} else {
		if ui.PrimaryButton(context.Key("start-recording"), text(context, "Start recording")).FillWidth().Disabled(disabled || !application.running || len(codecs) == 0).Clicked() {
			application.startRecording()
		}
	}
	if ui.Button(context.Key("refresh-encoders"), text(context, "Refresh recording GPUs")).FillWidth().Disabled(disabled).Clicked() {
		application.refreshRecordingDevices()
	}
	if application.status.Recording.Phase != "" && application.status.Recording.Phase != "idle" {
		helpText(context, recordingSummary(application.status.Recording, context))
		if application.status.Recording.Path != "" {
			helpText(context, application.status.Recording.Path)
		}
	}
}
