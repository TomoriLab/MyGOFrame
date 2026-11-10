//go:build linux && cgo

package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func presetFixture(name string) CapturePreset {
	fourcc, _ := FourCC("NV12")
	return CapturePreset{Name: name, DevicePath: "/dev/video-unavailable", DeviceName: "Capture",
		Mode:       VideoMode{FourCC: fourcc, Format: "NV12", Width: 1920, Height: 1080, IntervalNumerator: 1001, IntervalDenominator: 60000},
		ColorRange: "Full", Backend: "dma", Volume: .37, Muted: true}
}

func TestPresetBridgePersistenceAndRejectedApply(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	preset := presetFixture("Console 한글")
	document, err := Presets(PresetRequest{Operation: "save", Preset: &preset})
	if err != nil || len(document.Presets) != 1 {
		t.Fatalf("save: %+v %v", document, err)
	}
	document, err = Presets(PresetRequest{Operation: "list"})
	if err != nil || len(document.Presets) != 1 || document.Presets[0] != preset {
		t.Fatalf("reload: %+v %v", document, err)
	}
	preset.Volume = .83
	if _, err := Presets(PresetRequest{Operation: "update", Preset: &preset}); err != nil {
		t.Fatal(err)
	}
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	before, _ := engine.Snapshot()
	if _, err := engine.ApplyPreset(preset); err == nil {
		t.Fatal("unavailable device accepted")
	}
	after, _ := engine.Snapshot()
	if after.CommandID != before.CommandID || after.Generation != before.Generation {
		t.Fatal("rejected preset mutated session")
	}
	if _, err := Presets(PresetRequest{Operation: "delete", Name: preset.Name}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "mygoframe", "presets.json")
	damaged := []byte(`{"schemaVersion":99,"presets":[]}`)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Presets(PresetRequest{Operation: "save", Preset: &preset}); err == nil {
		t.Fatal("unsupported file overwritten")
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(damaged) {
		t.Fatal("failed write changed the file")
	}
}

func TestPresetStoreConcurrentProcesses(t *testing.T) {
	if name := os.Getenv("MYGOFRAME_PRESET_TEST_CHILD"); name != "" {
		preset := presetFixture(name)
		if _, err := Presets(PresetRequest{Operation: "save", Preset: &preset}); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	processes := make([]*exec.Cmd, 8)
	for index := range processes {
		processes[index] = exec.Command(os.Args[0], "-test.run=^TestPresetStoreConcurrentProcesses$")
		processes[index].Env = append(os.Environ(), fmt.Sprintf("MYGOFRAME_PRESET_TEST_CHILD=Console %d", index))
		if err := processes[index].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, process := range processes {
		if err := process.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	document, err := Presets(PresetRequest{Operation: "list"})
	if err != nil || len(document.Presets) != len(processes) {
		t.Fatalf("lost concurrent writes: %+v %v", document, err)
	}
}
