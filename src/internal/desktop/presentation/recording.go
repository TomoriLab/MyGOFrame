package presentation

import (
	"fmt"
	"strings"

	session "github.com/TomoriLab/MyGOFrame/src/internal/session/domain"

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
				return
			}
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
func recordingSummary(status session.RecordingSnapshot) string {
	if status.Error != "" {
		return "Recording failed: " + status.Error
	}
	return fmt.Sprintf("Recording %s · %.1f s · %.1f MiB · %d frames · %d dropped", status.Phase, float64(status.DurationNanoseconds)/1e9, float64(status.Bytes)/(1024*1024), status.Frames, status.DroppedFrames)
}
func (application *applicationState) recordingControls(context *ui.Context) {
	active := application.status.Recording.Active()
	disabled := active || application.busy || application.recordingProbeBusy
	ui.Text(context, "Video recording").FontSize(16).FontWeight(600)
	helpText(context, "Record the capture card while the preview and sharing window remain active.")
	labels := []string{"Automatic"}
	for _, device := range application.recordingDevices {
		labels = append(labels, recordingDeviceLabel(device))
	}
	field(context, "Recording GPU", func() {
		if ui.Select(context.Key("recording-gpu"), &application.recordingGPU, labels).FillWidth().Label("Recording GPU selection").Disabled(disabled).Changed() {
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
		ui.Select(context.Key("recording-codec"), &application.recordingConfig.Codec, codecs).FillWidth().Label("Recording codec").Disabled(disabled)
	})
	field(context, "Target bitrate (Mbps)", func() {
		ui.NumberInput(context.Key("recording-bitrate"), &application.recordingConfig.BitrateMbps, .1, 1000, .1).FillWidth().Label("Recording bitrate").Disabled(disabled)
	})
	field(context, "Rate control", func() {
		ui.Select(context.Key("recording-rate"), &application.recordingConfig.RateControl, []string{"vbr", "cbr"}).FillWidth().Label("Recording rate control").Disabled(disabled)
	})
	field(context, "Output file", func() {
		ui.TextInput(context.Key("recording-path"), &application.recordingConfig.Path).FillWidth().Placeholder("Automatic: ~/Videos/MyGOFrame/*.mkv").Label("Recording output path").Disabled(disabled)
	})
	helpText(context, "Use an absolute .mkv or .mp4 path. Existing files are never overwritten. Bitrate is a target and depends on the content.")
	ui.Checkbox(context.Key("recording-audio"), &application.recordingConfig.RecordAudio, "Record capture audio").Disabled(disabled)
	helpText(context, "Select the capture-card microphone input in Audio. Monitor volume and mute are independent of recording.")
	if active {
		if ui.PrimaryButton(context.Key("stop-recording"), "Stop recording").FillWidth().Disabled(application.busy || application.status.Recording.Phase == "stopping").Clicked() {
			application.stopRecording()
		}
	} else {
		if ui.PrimaryButton(context.Key("start-recording"), "Start recording").FillWidth().Disabled(disabled || !application.running || len(codecs) == 0).Clicked() {
			application.startRecording()
		}
	}
	if ui.Button(context.Key("refresh-encoders"), "Refresh recording GPUs").FillWidth().Disabled(disabled).Clicked() {
		application.refreshRecordingDevices()
	}
	if application.status.Recording.Phase != "" && application.status.Recording.Phase != "idle" {
		helpText(context, recordingSummary(application.status.Recording))
		if application.status.Recording.Path != "" {
			helpText(context, application.status.Recording.Path)
		}
	}
}
