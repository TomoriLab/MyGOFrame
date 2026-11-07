// Package graphicsruntime prepares process-scoped graphics compatibility settings.
package graphicsruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	externalFiles        = "__EGL_EXTERNAL_PLATFORM_CONFIG_FILENAMES"
	externalDirectories  = "__EGL_EXTERNAL_PLATFORM_CONFIG_DIRS"
	vendorFiles          = "__EGL_VENDOR_LIBRARY_FILENAMES"
	vendorDirectories    = "__EGL_VENDOR_LIBRARY_DIRS"
	disableDBus          = "__GL_DisableDBus"
	gbmBackend           = "GBM_BACKEND"
	gbmBackendsPath      = "GBM_BACKENDS_PATH"
	mesaDriver           = "MESA_LOADER_DRIVER_OVERRIDE"
	vulkanDriverFiles    = "VK_DRIVER_FILES"
	vulkanICDFiles       = "VK_ICD_FILENAMES"
	maximumFramesAllowed = "__GL_MaxFramesAllowed"
	legacyWayland        = "libnvidia-egl-wayland.so.1"
	modernWayland        = "libnvidia-egl-wayland2.so.1"
	testedDriverVersion  = "615.78.08"
	maximumManifestBytes = 1024 * 1024
)

// EnvironmentChange contains only the environment values applied by Prepare.
type EnvironmentChange map[string]string

type environment struct {
	lookup func(string) (string, bool)
	set    func(string, string) error
	unset  func(string) error
}

type manifest struct {
	path    string
	library string
}

// Prepare applies NVIDIA compatibility workarounds before graphics initialization.
// The workarounds apply only to the tested kernel-driver version 615.78.08, so
// newer drivers retain their normal platform and power-management behavior.
// Installed driver files and explicit environment settings remain unchanged.
func Prepare() (EnvironmentChange, error) {
	return prepare("/", environment{os.LookupEnv, os.Setenv, os.Unsetenv})
}

func prepare(root string, process environment) (EnvironmentChange, error) {
	changes, err := discover(root, process.lookup)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	applied := make(EnvironmentChange)
	for _, key := range keys {
		// A caller may have supplied a value after discovery completed.
		if _, present := process.lookup(key); present {
			continue
		}
		if err := process.set(key, changes[key]); err != nil {
			failures := []error{fmt.Errorf("set graphics environment %s: %w", key, err)}
			for previous := range applied {
				if rollbackError := process.unset(previous); rollbackError != nil {
					failures = append(failures, fmt.Errorf("restore graphics environment %s: %w", previous, rollbackError))
				}
			}
			return nil, errors.Join(failures...)
		}
		applied[key] = changes[key]
	}
	return applied, nil
}

func discover(root string, lookup func(string) (string, bool)) (EnvironmentChange, error) {
	_, explicitDBus := lookup(disableDBus)
	selectionAllowed := waylandSelected(lookup)
	for _, key := range []string{externalFiles, externalDirectories, vendorFiles, vendorDirectories, gbmBackend, gbmBackendsPath, mesaDriver, vulkanDriverFiles, vulkanICDFiles} {
		if _, present := lookup(key); present {
			selectionAllowed = false
		}
	}
	if !selectionAllowed && explicitDBus {
		return EnvironmentChange{}, nil
	}
	versionPath := filepath.Join(root, "sys/module/nvidia/version")
	version, err := os.ReadFile(versionPath)
	if errors.Is(err, os.ErrNotExist) {
		return EnvironmentChange{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read NVIDIA driver version %q: %w", versionPath, err)
	}
	if strings.TrimSpace(string(version)) != testedDriverVersion {
		return EnvironmentChange{}, nil
	}
	// NVIDIA's loader searches both directories unless an explicit override is set.
	platforms, err := readManifests(root, []string{
		"etc/egl/egl_external_platform.d",
		"usr/share/egl/egl_external_platform.d",
	})
	if err != nil {
		return nil, err
	}
	var hasLegacy, hasModern, hasNVIDIA bool
	for _, platform := range platforms {
		switch platform.library {
		case legacyWayland:
			hasLegacy, hasNVIDIA = true, true
		case modernWayland:
			hasModern, hasNVIDIA = true, true
		}
	}
	if !explicitDBus {
		vendors, err := readManifests(root, []string{
			"etc/glvnd/egl_vendor.d",
			"usr/share/glvnd/egl_vendor.d",
		})
		if err != nil {
			return nil, err
		}
		for _, vendor := range vendors {
			if vendor.library == "libEGL_nvidia.so.0" {
				hasNVIDIA = true
			}
		}
	}
	changes := make(EnvironmentChange)
	if hasNVIDIA && !explicitDBus {
		// The vendor setting avoids its unreleased private-bus ListNames reply.
		changes[disableDBus] = "1"
	}
	if selectionAllowed && hasModern && mesaRuntimeAvailable(root) {
		// Mesa's allocator avoids the tested NVIDIA GBM leak while retaining Wayland2.
		changes[gbmBackend] = "dri"
		changes[mesaDriver] = "zink"
		if _, explicit := lookup(maximumFramesAllowed); !explicit {
			changes[maximumFramesAllowed] = "1"
		}
	} else if selectionAllowed && hasLegacy {
		if hasModern {
			selected := make([]string, 0, len(platforms))
			for _, platform := range platforms {
				if platform.library != modernWayland {
					selected = append(selected, platform.path)
				}
			}
			// Other platforms remain available for GBM, X11, and independent vendors.
			changes[externalFiles] = strings.Join(selected, string(os.PathListSeparator))
		}
		if _, explicit := lookup(maximumFramesAllowed); !explicit {
			changes[maximumFramesAllowed] = "1"
		}
	}
	return changes, nil
}

func nativeLibraryDirectories() []string {
	var triplet string
	switch runtime.GOARCH {
	case "amd64":
		triplet = "x86_64-linux-gnu"
	case "arm64":
		triplet = "aarch64-linux-gnu"
	case "386":
		triplet = "i386-linux-gnu"
	case "arm":
		triplet = "arm-linux-gnueabihf"
	case "ppc64le":
		triplet = "powerpc64le-linux-gnu"
	case "riscv64":
		triplet = "riscv64-linux-gnu"
	case "s390x":
		triplet = "s390x-linux-gnu"
	}
	var directories []string
	if triplet != "" {
		directories = append(directories, "usr/lib/"+triplet, "lib/"+triplet)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64", "ppc64le", "ppc64", "riscv64", "s390x", "loong64", "mips64", "mips64le":
		directories = append(directories, "usr/lib64", "usr/lib", "lib64", "lib")
		return directories
	}
	return append(directories, "usr/lib", "lib")
}

func mesaRuntimeAvailable(root string) bool {
	for _, directory := range nativeLibraryDirectories() {
		complete := true
		for _, relative := range []string{"gbm/dri_gbm.so", "dri/zink_dri.so", "libvulkan.so.1"} {
			information, err := os.Stat(filepath.Join(root, directory, relative))
			if err != nil || !information.Mode().IsRegular() {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}

func waylandSelected(lookup func(string) (string, bool)) bool {
	if backends, present := lookup("GDK_BACKEND"); present {
		for _, backend := range strings.Split(backends, ",") {
			backend = strings.TrimSpace(backend)
			if backend == "" {
				continue
			}
			if backend != "*" {
				return backend == "wayland"
			}
			break
		}
	}
	for _, key := range []string{"WAYLAND_DISPLAY", "WAYLAND_SOCKET"} {
		if value, _ := lookup(key); value != "" {
			return true
		}
	}
	value, _ := lookup("XDG_SESSION_TYPE")
	return strings.EqualFold(value, "wayland")
}

func readManifests(root string, directories []string) ([]manifest, error) {
	var manifests []manifest
	for _, relative := range directories {
		directory := filepath.Join(root, relative)
		entries, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read graphics manifest directory %q: %w", directory, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			library, err := readManifest(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if strings.ContainsRune(path, os.PathListSeparator) {
				return nil, fmt.Errorf("graphics manifest path %q contains the platform-list separator", path)
			}
			manifests = append(manifests, manifest{path, filepath.Base(library)})
		}
	}
	return manifests, nil
}

func readManifest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open graphics manifest %q: %w", path, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumManifestBytes+1))
	if err != nil {
		return "", fmt.Errorf("read graphics manifest %q: %w", path, err)
	}
	if len(data) > maximumManifestBytes {
		return "", fmt.Errorf("graphics manifest %q exceeds the supported size", path)
	}
	var document struct {
		Version string `json:"file_format_version"`
		ICD     struct {
			Library string `json:"library_path"`
		} `json:"ICD"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("decode graphics manifest %q: %w", path, err)
	}
	if document.Version != "1.0.0" || document.ICD.Library == "" || strings.ContainsRune(document.ICD.Library, 0) {
		return "", fmt.Errorf("graphics manifest %q has an unsupported version or library path", path)
	}
	return document.ICD.Library, nil
}
