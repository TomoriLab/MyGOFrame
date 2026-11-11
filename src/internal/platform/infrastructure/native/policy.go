// Package nativeruntime selects the application's verified private native libraries.
package nativeruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

const (
	modeVariable             = "MYGOFRAME_NATIVE_RUNTIME"
	directoryVariable        = "MYGOFRAME_NATIVE_RUNTIME_DIR"
	activeVariable           = "MYGOFRAME_NATIVE_RUNTIME_ACTIVE"
	moduleVariable           = "GTK_PATH"
	moduleActiveVariable     = "MYGOFRAME_GTK_MODULE_RUNTIME_ACTIVE"
	eventPaintVariable       = "MYGO_GDK_EVENT_DRIVEN_PAINT"
	eventPaintActiveVariable = "MYGOFRAME_EVENT_PAINT_RUNTIME_ACTIVE"
	textureVariable          = "MYGO_GL_GLES_OPAQUE_TEXTURE"
	textureActiveVariable    = "MYGOFRAME_GLES_TEXTURE_RUNTIME_ACTIVE"
	loaderVariable           = "LD_LIBRARY_PATH"
	maximumManifest          = 1024 * 1024
	maximumLibrary           = 128 * 1024 * 1024
)

type library struct {
	File    string `json:"file"`
	SONAME  string `json:"soname"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type manifest struct {
	SchemaVersion       int                `json:"schemaVersion"`
	Architecture        string             `json:"architecture"`
	Multiarch           string             `json:"multiarch"`
	Libraries           map[string]library `json:"libraries"`
	GTKModules          map[string]library `json:"gtkModules"`
	SystemCompatibility map[string]library `json:"systemCompatibility"`
	Features            map[string]bool    `json:"features"`
}

type launch struct {
	Environment []string
	Directory   string
}

type configuration struct {
	Executable   string
	Environment  []string
	Architecture string
	ResolveGTK   func(map[string]string) (string, error)
}

func plan(config configuration) (*launch, error) {
	values := environmentValues(config.Environment)
	mode := values[modeVariable]
	if mode != "" && mode != "auto" && mode != "system" {
		return nil, fmt.Errorf("%s must be auto or system", modeVariable)
	}
	active := values[activeVariable]
	if active != "" && (!filepath.IsAbs(active) || strings.ContainsAny(active, ":;$")) {
		return nil, errors.New("invalid native runtime activation marker")
	}
	if active := values[moduleActiveVariable]; active != "" && (!filepath.IsAbs(active) || strings.ContainsAny(active, ":;$")) {
		return nil, errors.New("invalid GTK module runtime activation marker")
	}
	if mode == "system" || values["LD_PRELOAD"] != "" || values["LD_AUDIT"] != "" {
		return systemLaunch(config.Environment, values), nil
	}
	if strings.ContainsAny(values[loaderVariable], "$;") {
		if _, explicit := values[directoryVariable]; explicit {
			return nil, errors.New("an explicit private native runtime requires LD_LIBRARY_PATH without dynamic tokens or semicolon separators")
		}
		return systemLaunch(config.Environment, values), nil
	}
	directories, explicit, err := runtimeDirectories(config.Executable, values, config.Architecture)
	if err != nil {
		return nil, err
	}
	for _, directory := range directories {
		contents, err := readManifest(filepath.Join(directory, "manifest.json"))
		if errors.Is(err, os.ErrNotExist) && !explicit {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read private native runtime: %w", err)
		}
		if strings.ContainsAny(directory, ":;$") {
			return nil, fmt.Errorf("private native runtime directory contains loader separators or tokens; select %s=system", modeVariable)
		}
		var metadata manifest
		if err := json.Unmarshal(contents, &metadata); err != nil {
			return nil, fmt.Errorf("decode private native runtime: %w", err)
		}
		if err := validate(directory, metadata, config.Architecture, values, config.ResolveGTK); err != nil {
			return nil, fmt.Errorf("private native runtime: %w; rebuild it or select %s=system", err, modeVariable)
		}
		first, _, _ := strings.Cut(values[loaderVariable], ":")
		if first != directory && active == directory {
			return nil, errors.New("private native runtime re-execution did not preserve its loader path")
		}
		environment := config.Environment
		changed := first != directory
		if changed {
			base := removeDirectory(values[loaderVariable], active)
			path := directory
			if base != "" {
				path += ":" + base
			}
			environment = replaceVariable(environment, loaderVariable, path, false)
			environment = replaceVariable(environment, activeVariable, directory, false)
		}
		environment, moduleChanged, err := moduleEnvironment(environment, directory, len(metadata.GTKModules) != 0)
		if err != nil {
			return nil, err
		}
		environment, textureChanged := capabilityEnvironment(environment, metadata.Features["glesOpaqueTexture"], textureVariable, textureActiveVariable)
		environment, eventPaintChanged := capabilityEnvironment(environment, metadata.Features["eventDrivenPaint"], eventPaintVariable, eventPaintActiveVariable)
		if !changed && !moduleChanged && !textureChanged && !eventPaintChanged {
			return nil, nil
		}
		return &launch{Environment: environment, Directory: directory}, nil
	}
	return systemLaunch(config.Environment, values), nil
}

func systemLaunch(environment []string, values map[string]string) *launch {
	if values[activeVariable] == "" && values[moduleActiveVariable] == "" && values[textureActiveVariable] == "" && values[eventPaintActiveVariable] == "" {
		return nil
	}
	result := environment
	if values[activeVariable] != "" {
		path := removeDirectory(values[loaderVariable], values[activeVariable])
		result = replaceVariable(result, activeVariable, "", true)
		result = replaceVariable(result, loaderVariable, path, path == "")
	}
	result = removeModuleEnvironment(result)
	result, _ = capabilityEnvironment(result, false, textureVariable, textureActiveVariable)
	result, _ = capabilityEnvironment(result, false, eventPaintVariable, eventPaintActiveVariable)
	return &launch{Environment: result}
}

// Verified runtime capabilities preserve explicit caller overrides and retire managed settings on downgrade.
func capabilityEnvironment(environment []string, available bool, settingVariable, ownershipVariable string) ([]string, bool) {
	values := environmentValues(environment)
	managed := values[ownershipVariable] != ""
	if managed && values[settingVariable] != "1" {
		// A caller changed the inherited setting after activation and now owns it.
		return replaceVariable(environment, ownershipVariable, "", true), true
	}
	if !available {
		if !managed {
			return environment, false
		}
		result := replaceVariable(environment, settingVariable, "", true)
		return replaceVariable(result, ownershipVariable, "", true), true
	}
	if _, explicit := values[settingVariable]; explicit && !managed {
		return environment, false
	}
	if managed && values[settingVariable] == "1" {
		return environment, false
	}
	result := replaceVariable(environment, settingVariable, "1", false)
	return replaceVariable(result, ownershipVariable, "1", false), true
}

func removeModuleEnvironment(environment []string) []string {
	values := environmentValues(environment)
	if values[moduleActiveVariable] == "" {
		return environment
	}
	path := removeDirectory(values[moduleVariable], values[moduleActiveVariable])
	result := replaceVariable(environment, moduleActiveVariable, "", true)
	return replaceVariable(result, moduleVariable, path, path == "")
}

func moduleEnvironment(environment []string, directory string, available bool) ([]string, bool, error) {
	values := environmentValues(environment)
	active := values[moduleActiveVariable]
	if !available || values["GTK_EXE_PREFIX"] != "" || (active == "" && values[moduleVariable] != "") {
		return removeModuleEnvironment(environment), active != "", nil
	}
	directory = filepath.Join(directory, "gtk-3.0")
	first, _, _ := strings.Cut(values[moduleVariable], ":")
	if active == directory {
		if first != directory {
			return nil, false, errors.New("GTK module runtime re-execution did not preserve its module path")
		}
		return environment, false, nil
	}
	path := directory
	if base := removeDirectory(values[moduleVariable], active); base != "" {
		path += ":" + base
	}
	result := replaceVariable(environment, moduleVariable, path, false)
	result = replaceVariable(result, moduleActiveVariable, directory, false)
	return result, true, nil
}

func runtimeDirectories(executable string, values map[string]string, architecture string) ([]string, bool, error) {
	if directory, present := values[directoryVariable]; present {
		if directory == "" {
			return nil, true, errors.New("the private native runtime directory must not be empty")
		}
		absolute, err := filepath.Abs(directory)
		if err != nil || strings.ContainsAny(absolute, ":;$") {
			return nil, true, errors.New("the private native runtime directory cannot be represented in the loader path")
		}
		return []string{absolute}, true, nil
	}
	triplet := multiarch(architecture)
	if triplet == "" {
		return nil, false, nil
	}
	directory := filepath.Dir(executable)
	return []string{
		filepath.Join(directory, "native-runtime"),
		filepath.Join(directory, "native"),
		filepath.Join(directory, "..", "lib", triplet, "mygoframe", "native"),
		filepath.Join(directory, "..", "lib", "mygoframe", "native"),
	}, false, nil
}

func multiarch(architecture string) string {
	switch architecture {
	case "amd64":
		return "x86_64-linux-gnu"
	case "arm64":
		return "aarch64-linux-gnu"
	}
	return ""
}

func readManifest(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximumManifest+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumManifest {
		return nil, errors.New("native runtime manifest exceeds its size limit")
	}
	return contents, nil
}

func validate(directory string, metadata manifest, architecture string, values map[string]string, resolveGTK func(map[string]string) (string, error)) error {
	if multiarch(architecture) == "" || metadata.SchemaVersion != 1 || metadata.Architecture != architecture || metadata.Multiarch != multiarch(architecture) {
		return errors.New("manifest schema or native architecture does not match this executable")
	}
	if len(metadata.Libraries) != 3 {
		return errors.New("manifest must describe exactly the GTK, GDK, and Fcitx libraries")
	}
	allowed := map[string]bool{}
	for name, soname := range map[string]string{"gtk": "libgtk-3.so.0", "gdk": "libgdk-3.so.0", "fcitx": "libFcitx5GClient.so.2"} {
		component, present := metadata.Libraries[name]
		if !present || component.SONAME != soname || !basename(component.File) || !digest(component.SHA256) {
			return fmt.Errorf("invalid %s library metadata", name)
		}
		path, err := containedFile(directory, component.File)
		if err != nil {
			return err
		}
		alias, err := containedFile(directory, soname)
		if err != nil || alias != path {
			return fmt.Errorf("%s SONAME does not resolve to its verified library", name)
		}
		actual, err := fileDigest(path)
		if err != nil || !strings.EqualFold(actual, component.SHA256) {
			return fmt.Errorf("%s library does not match its manifest hash", name)
		}
		allowed[component.File], allowed[soname] = true, true
	}
	if len(metadata.GTKModules) != 0 {
		if len(metadata.GTKModules) != 2 {
			return errors.New("manifest must describe both supported GTK modules")
		}
		for name, filename := range map[string]string{"colorreload": "libcolorreload-gtk-module.so", "windowDecorations": "libwindow-decorations-gtk-module.so"} {
			component, present := metadata.GTKModules[name]
			expected := filepath.Join("gtk-3.0", "modules", filename)
			if !present || component.File != expected || !digest(component.SHA256) {
				return fmt.Errorf("invalid %s GTK module metadata", name)
			}
			path, err := containedFile(directory, component.File)
			if err != nil {
				return err
			}
			actual, err := fileDigest(path)
			if err != nil || !strings.EqualFold(actual, component.SHA256) {
				return fmt.Errorf("%s GTK module does not match its manifest hash", name)
			}
			allowed[expected] = true
		}
	}
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if !allowed[relative] && (strings.Contains(entry.Name(), ".so") || entry.Type()&os.ModeSymlink != 0) {
			return fmt.Errorf("unexpected shared library or symbolic link %q in private runtime", relative)
		}
		return nil
	}); err != nil {
		return err
	}
	gtk, present := metadata.SystemCompatibility["gtk"]
	if !present || gtk.SONAME != "libgtk-3.so.0" || !digest(gtk.SHA256) {
		return errors.New("manifest has no verified system GTK compatibility record")
	}
	// Compatibility describes the system baseline used to build the entire bundle.
	// The selected private GTK must not be mistaken for that baseline after exec.
	systemValues := maps.Clone(values)
	systemValues[loaderVariable] = removeDirectory(values[loaderVariable], directory)
	systemValues[loaderVariable] = removeDirectory(systemValues[loaderVariable], values[activeVariable])
	path, err := resolveGTK(systemValues)
	if err != nil {
		return fmt.Errorf("resolve system GTK: %w", err)
	}
	actual, err := fileDigest(path)
	if err != nil || !strings.EqualFold(actual, gtk.SHA256) {
		return errors.New("system GTK does not match the private GDK build")
	}
	return nil
}

func basename(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name
}

func containedFile(directory, name string) (string, error) {
	base, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(directory, name))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("private runtime library escapes its directory")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("private runtime library is not a regular file")
	}
	return path, nil
}

func digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumLibrary {
		return "", errors.New("native library is not a bounded regular file")
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, maximumLibrary+1))
	if err != nil {
		return "", err
	}
	if count > maximumLibrary {
		return "", errors.New("native library exceeds its size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range environment {
		if key, value, present := strings.Cut(entry, "="); present {
			values[key] = value
		}
	}
	return values
}

func replaceVariable(environment []string, key, value string, remove bool) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, key+"=") {
			result = append(result, entry)
		}
	}
	if !remove {
		result = append(result, key+"="+value)
	}
	return result
}

func removeDirectory(path, directory string) string {
	if directory == "" {
		return path
	}
	parts := strings.Split(path, ":")
	result := parts[:0]
	for _, part := range parts {
		if part != directory {
			result = append(result, part)
		}
	}
	return strings.Join(result, ":")
}
