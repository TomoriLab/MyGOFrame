package graphicsruntime

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type policyCase struct {
	Name          string            `json:"name"`
	DriverVersion *string           `json:"driverVersion"`
	RuntimeFiles  []string          `json:"runtimeFiles"`
	Environment   map[string]string `json:"environment"`
	Manifests     []struct {
		Path    string `json:"path"`
		Library string `json:"library"`
	} `json:"manifests"`
	InvalidFiles    map[string]string `json:"invalidFiles"`
	ExpectedChanges map[string]string `json:"expectedChanges"`
	ErrorContains   string            `json:"errorContains"`
}

func fakeEnvironment(values map[string]string) environment {
	return environment{
		lookup: func(key string) (string, bool) { value, present := values[key]; return value, present },
		set:    func(key, value string) error { values[key] = value; return nil },
		unset:  func(key string) error { delete(values, key); return nil },
	}
}

func writeFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/policy-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []policyCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			root := t.TempDir()
			version := testedDriverVersion
			if fixture.DriverVersion != nil {
				version = *fixture.DriverVersion
			}
			if version != "" {
				writeFile(t, root, "sys/module/nvidia/version", []byte(version+"\n"))
			}
			for _, relative := range fixture.RuntimeFiles {
				writeFile(t, root, relative, nil)
			}
			for _, entry := range fixture.Manifests {
				encoded, err := json.Marshal(map[string]any{
					"file_format_version": "1.0.0", "ICD": map[string]string{"library_path": entry.Library},
				})
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, root, entry.Path, encoded)
			}
			for relative, text := range fixture.InvalidFiles {
				writeFile(t, root, relative, []byte(text))
			}
			values := maps.Clone(fixture.Environment)
			if values == nil {
				values = make(map[string]string)
			}
			before := maps.Clone(values)
			changes, err := prepare(root, fakeEnvironment(values))
			if fixture.ErrorContains != "" {
				if err == nil || !strings.Contains(err.Error(), fixture.ErrorContains) {
					t.Fatalf("error = %v, expected %q", err, fixture.ErrorContains)
				}
				if !maps.Equal(values, before) {
					t.Fatalf("failed discovery mutated the environment: %v", values)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := make(EnvironmentChange)
			for key, value := range fixture.ExpectedChanges {
				expected[key] = strings.ReplaceAll(value, "${ROOT}", root)
			}
			if !maps.Equal(changes, expected) {
				t.Fatalf("changes = %v, expected %v", changes, expected)
			}
			maps.Copy(before, expected)
			if !maps.Equal(values, before) {
				t.Fatalf("environment = %v, expected %v", values, before)
			}
		})
	}
}

func TestWaylandSelection(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   map[string]string
		selected bool
	}{
		{"socket", map[string]string{"WAYLAND_SOCKET": "7"}, true},
		{"wildcard", map[string]string{"GDK_BACKEND": "*,x11", "WAYLAND_DISPLAY": "wayland-0"}, true},
		{"other_backend", map[string]string{"GDK_BACKEND": "broadway", "WAYLAND_DISPLAY": "wayland-0"}, false},
		{"empty_selection", map[string]string{"GDK_BACKEND": "", "XDG_SESSION_TYPE": "Wayland"}, true},
		{"empty_tokens", map[string]string{"GDK_BACKEND": " , wayland ,x11"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if selected := waylandSelected(fakeEnvironment(test.values).lookup); selected != test.selected {
				t.Fatalf("selection = %t, expected %t", selected, test.selected)
			}
		})
	}
}

func TestSettingFailureRestoresEnvironment(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sys/module/nvidia/version", []byte(testedDriverVersion+"\n"))
	for _, library := range []string{legacyWayland, modernWayland} {
		writeFile(t, root, "usr/share/egl/egl_external_platform.d/"+library+".json", []byte(
			`{"file_format_version":"1.0.0","ICD":{"library_path":"`+library+`"}}`))
	}
	values := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	before := maps.Clone(values)
	process := fakeEnvironment(values)
	process.set = func(key, value string) error {
		if key == disableDBus {
			return errors.New("injected environment failure")
		}
		values[key] = value
		return nil
	}
	if _, err := prepare(root, process); err == nil {
		t.Fatal("environment failure was not reported")
	}
	if !maps.Equal(values, before) {
		t.Fatalf("partial environment changes remained: %v", values)
	}
}

func TestInvalidManifestMetadata(t *testing.T) {
	for _, document := range []string{
		`{"file_format_version":"2.0.0","ICD":{"library_path":"libEGL_nvidia.so.0"}}`,
		`{"file_format_version":"1.0.0","ICD":{"library_path":""}}`,
		`{"file_format_version":"1.0.0","ICD":{"library_path":"bad\u0000path"}}`,
		strings.Repeat(" ", maximumManifestBytes+1),
	} {
		root := t.TempDir()
		writeFile(t, root, "sys/module/nvidia/version", []byte(testedDriverVersion+"\n"))
		writeFile(t, root, "usr/share/egl/egl_external_platform.d/invalid.json", []byte(document))
		values := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
		if _, err := prepare(root, fakeEnvironment(values)); err == nil {
			t.Fatal("invalid manifest was accepted")
		}
		if len(values) != 1 {
			t.Fatalf("invalid manifest changed the environment: %v", values)
		}
	}
}

func TestMesaRuntimeFollowsLibrarySymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "usr/lib/gbm/dri_gbm.so", nil)
	writeFile(t, root, "usr/lib/dri/driver-target.so", nil)
	writeFile(t, root, "usr/lib/libvulkan.so.1", nil)
	if err := os.Symlink("driver-target.so", filepath.Join(root, "usr/lib/dri/zink_dri.so")); err != nil {
		t.Fatal(err)
	}
	if !mesaRuntimeAvailable(root) {
		t.Fatal("installed library symlinks were not recognized")
	}
	if err := os.Remove(filepath.Join(root, "usr/lib/dri/driver-target.so")); err != nil {
		t.Fatal(err)
	}
	if mesaRuntimeAvailable(root) {
		t.Fatal("a broken driver symlink was treated as an installed runtime")
	}
}

func TestMesaRuntimeRejectsLibraryDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "usr/lib/gbm/dri_gbm.so", nil)
	writeFile(t, root, "usr/lib/dri/zink_dri.so", nil)
	if err := os.MkdirAll(filepath.Join(root, "usr/lib/libvulkan.so.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if mesaRuntimeAvailable(root) {
		t.Fatal("a directory was treated as an installed Vulkan loader")
	}
}
