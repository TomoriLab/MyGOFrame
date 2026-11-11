package nativeruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fixture struct {
	config    configuration
	directory string
	gtk       string
	metadata  manifest
}

func runtimeFixture(test *testing.T) fixture {
	test.Helper()
	root := test.TempDir()
	directory := filepath.Join(root, "build with spaces", "native-runtime")
	if err := os.MkdirAll(directory, 0755); err != nil {
		test.Fatal(err)
	}
	gtk := filepath.Join(root, "libgtk-3.so.0")
	if err := os.WriteFile(gtk, []byte("the compatible system GTK"), 0644); err != nil {
		test.Fatal(err)
	}
	metadata := manifest{
		SchemaVersion: 1, Architecture: "amd64", Multiarch: "x86_64-linux-gnu",
		Libraries: make(map[string]library), SystemCompatibility: make(map[string]library),
	}
	for name, soname := range map[string]string{"gtk": "libgtk-3.so.0", "gdk": "libgdk-3.so.0", "fcitx": "libFcitx5GClient.so.2"} {
		filename := soname + ".verified"
		path := filepath.Join(directory, filename)
		if err := os.WriteFile(path, []byte("verified "+name), 0644); err != nil {
			test.Fatal(err)
		}
		if err := os.Symlink(filename, filepath.Join(directory, soname)); err != nil {
			test.Fatal(err)
		}
		hash, err := fileDigest(path)
		if err != nil {
			test.Fatal(err)
		}
		metadata.Libraries[name] = library{File: filename, SONAME: soname, SHA256: hash}
	}
	hash, err := fileDigest(gtk)
	if err != nil {
		test.Fatal(err)
	}
	metadata.SystemCompatibility["gtk"] = library{SONAME: "libgtk-3.so.0", SHA256: hash}
	value := fixture{
		directory: directory, gtk: gtk, metadata: metadata,
		config: configuration{
			Executable: filepath.Join(filepath.Dir(directory), "mygoframe"), Architecture: "amd64",
			Environment: []string{"UNRELATED=value", "LD_LIBRARY_PATH=/custom directory"},
			ResolveGTK:  func(map[string]string) (string, error) { return gtk, nil },
		},
	}
	value.write(test)
	return value
}

func (value fixture) write(test *testing.T) {
	test.Helper()
	contents, err := json.Marshal(value.metadata)
	if err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(value.directory, "manifest.json"), contents, 0644); err != nil {
		test.Fatal(err)
	}
}

func TestGLESOpaqueTextureCapabilityFollowsVerifiedRuntime(test *testing.T) {
	value := runtimeFixture(test)
	first, err := plan(value.config)
	if err != nil || first == nil || environmentValues(first.Environment)[textureVariable] != "" {
		test.Fatalf("an older runtime enabled opaque GLES textures: %v, %v", first, err)
	}
	value.metadata.Features = map[string]bool{"glesOpaqueTexture": true}
	value.write(test)
	value.config.Environment = first.Environment
	selected, err := plan(value.config)
	if err != nil || selected == nil || environmentValues(selected.Environment)[textureVariable] != "1" {
		test.Fatalf("verified GLES texture activation failed: %v, %v", selected, err)
	}
	value.config.Environment = selected.Environment
	if repeated, err := plan(value.config); err != nil || repeated != nil {
		test.Fatalf("GLES textures caused repeated execution: %v, %v", repeated, err)
	}
	value.config.Environment = replaceVariable(selected.Environment, textureVariable, "0", false)
	overridden, err := plan(value.config)
	if err != nil || overridden == nil || environmentValues(overridden.Environment)[textureVariable] != "0" || environmentValues(overridden.Environment)[textureActiveVariable] != "" {
		test.Fatalf("an inherited capability overrode the caller's selection: %v, %v", overridden, err)
	}
	value.config.Environment = selected.Environment
	value.metadata.Features = nil
	value.write(test)
	retired, err := plan(value.config)
	if err != nil || retired == nil || environmentValues(retired.Environment)[textureVariable] != "" {
		test.Fatalf("runtime downgrade retained its texture capability: %v, %v", retired, err)
	}
	value.config.Environment = append(selected.Environment, modeVariable+"=system")
	system, err := plan(value.config)
	if err != nil || system == nil || environmentValues(system.Environment)[textureVariable] != "" || environmentValues(system.Environment)[textureActiveVariable] != "" {
		test.Fatalf("system selection retained managed GLES textures: %v, %v", system, err)
	}
}

func TestExplicitGLESOpaqueTextureSelectionIsPreserved(test *testing.T) {
	for _, setting := range []string{"", "0", "1"} {
		value := runtimeFixture(test)
		value.metadata.Features = map[string]bool{"glesOpaqueTexture": true}
		value.write(test)
		value.config.Environment = append(value.config.Environment, textureVariable+"="+setting)
		selected, err := plan(value.config)
		if err != nil || selected == nil {
			test.Fatalf("explicit texture selection failed: %v, %v", selected, err)
		}
		values := environmentValues(selected.Environment)
		if values[textureVariable] != setting || values[textureActiveVariable] != "" {
			test.Fatalf("explicit texture selection was replaced: %v", values)
		}
	}
}

func (value *fixture) withModules(test *testing.T) {
	test.Helper()
	value.metadata.GTKModules = make(map[string]library)
	for name, filename := range map[string]string{"colorreload": "libcolorreload-gtk-module.so", "windowDecorations": "libwindow-decorations-gtk-module.so"} {
		relative := filepath.Join("gtk-3.0", "modules", filename)
		path := filepath.Join(value.directory, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			test.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("verified "+name), 0644); err != nil {
			test.Fatal(err)
		}
		hash, err := fileDigest(path)
		if err != nil {
			test.Fatal(err)
		}
		value.metadata.GTKModules[name] = library{File: relative, SHA256: hash}
	}
	value.write(test)
}

func TestGTKModulesActivateOnceAndSystemModeReleasesPaths(test *testing.T) {
	value := runtimeFixture(test)
	value.withModules(test)
	first, err := plan(value.config)
	if err != nil || first == nil {
		test.Fatalf("module activation = %v, %v", first, err)
	}
	variables := environmentValues(first.Environment)
	expected := filepath.Join(value.directory, "gtk-3.0")
	if variables[moduleVariable] != expected || variables[moduleActiveVariable] != expected {
		test.Fatalf("incorrect GTK module activation: %v", variables)
	}
	value.config.Environment = first.Environment
	if second, err := plan(value.config); err != nil || second != nil {
		test.Fatalf("GTK modules reactivated: %v, %v", second, err)
	}
	value.config.Environment = append(value.config.Environment, modeVariable+"=system")
	selected, err := plan(value.config)
	if err != nil || selected == nil {
		test.Fatalf("GTK module system selection = %v, %v", selected, err)
	}
	variables = environmentValues(selected.Environment)
	if variables[moduleVariable] != "" || variables[moduleActiveVariable] != "" || variables[activeVariable] != "" || variables[loaderVariable] != "/custom directory" {
		test.Fatalf("managed paths survived system selection: %v", variables)
	}
}

func TestExplicitGTKModuleSelectionRemainsUnderUserControl(test *testing.T) {
	for _, selection := range []string{"GTK_PATH=/user/modules", "GTK_EXE_PREFIX=/custom/gtk"} {
		value := runtimeFixture(test)
		value.withModules(test)
		value.config.Environment = append(value.config.Environment, selection, "GTK_MODULES=existing-module:/absolute/module.so")
		selected, err := plan(value.config)
		if err != nil || selected == nil {
			test.Fatalf("private libraries failed to activate: %v, %v", selected, err)
		}
		variables := environmentValues(selected.Environment)
		key, content, _ := strings.Cut(selection, "=")
		if variables[key] != content || variables[moduleActiveVariable] != "" || variables["GTK_MODULES"] != "existing-module:/absolute/module.so" {
			test.Fatalf("user GTK module selection changed: %v", variables)
		}
	}
}

func TestModuleOnlyActivationDoesNotClaimUserLoaderPath(test *testing.T) {
	value := runtimeFixture(test)
	value.withModules(test)
	value.config.Environment = []string{loaderVariable + "=" + value.directory + ":/custom"}
	selected, err := plan(value.config)
	if err != nil || selected == nil {
		test.Fatalf("module-only activation = %v, %v", selected, err)
	}
	variables := environmentValues(selected.Environment)
	if variables[activeVariable] != "" {
		test.Fatal("the user loader prefix was claimed by the application")
	}
	value.config.Environment = append(selected.Environment, modeVariable+"=system")
	selected, err = plan(value.config)
	if err != nil || selected == nil || environmentValues(selected.Environment)[loaderVariable] != value.directory+":/custom" {
		test.Fatalf("system mode removed a user loader path: %v, %v", selected, err)
	}
}

func TestGTKModulePathLossStopsReexecution(test *testing.T) {
	value := runtimeFixture(test)
	value.withModules(test)
	value.config.Environment = append(value.config.Environment, moduleActiveVariable+"="+filepath.Join(value.directory, "gtk-3.0"))
	if selected, err := plan(value.config); err == nil || selected != nil {
		test.Fatalf("lost GTK module path can reexecute indefinitely: %v, %v", selected, err)
	}
}

func TestCompatibleRuntimeDirectorySwitchRetiresPreviousPaths(test *testing.T) {
	previous := runtimeFixture(test)
	previous.withModules(test)
	activated, err := plan(previous.config)
	if err != nil || activated == nil {
		test.Fatalf("previous activation = %v, %v", activated, err)
	}
	selected := runtimeFixture(test)
	selected.withModules(test)
	selected.config.Environment = append(activated.Environment, directoryVariable+"="+selected.directory)
	selected.config.ResolveGTK = func(variables map[string]string) (string, error) {
		if variables[loaderVariable] != "/custom directory" {
			test.Fatalf("previous private GTK used as system baseline: %v", variables)
		}
		return selected.gtk, nil
	}
	next, err := plan(selected.config)
	if err != nil || next == nil {
		test.Fatalf("compatible directory switch = %v, %v", next, err)
	}
	variables := environmentValues(next.Environment)
	if variables[loaderVariable] != selected.directory+":/custom directory" || variables[moduleVariable] != filepath.Join(selected.directory, "gtk-3.0") {
		test.Fatalf("previous managed paths survived: %v", variables)
	}
	selected.config.Environment = next.Environment
	if next, err := plan(selected.config); err != nil || next != nil {
		test.Fatalf("directory switch repeated: %v, %v", next, err)
	}
}

func TestGTKModuleIntegrityAndAlternateSearchPathsAreChecked(test *testing.T) {
	cases := []string{"hash", "filename", "missing", "escaping symlink", "alternate module", "hardware library", "directory symlink"}
	for _, name := range cases {
		test.Run(name, func(test *testing.T) {
			value := runtimeFixture(test)
			value.withModules(test)
			component := value.metadata.GTKModules["colorreload"]
			switch name {
			case "hash":
				component.SHA256 = strings.Repeat("0", 64)
			case "filename":
				component.File = "../module.so"
			case "missing":
				delete(value.metadata.GTKModules, "windowDecorations")
			case "escaping symlink":
				path := filepath.Join(value.directory, component.File)
				if err := os.Remove(path); err != nil {
					test.Fatal(err)
				}
				if err := os.Symlink(value.gtk, path); err != nil {
					test.Fatal(err)
				}
			case "alternate module", "hardware library":
				relative := filepath.Join("gtk-3.0", "3.0.0", "modules", "libcolorreload-gtk-module.so")
				if name == "hardware library" {
					relative = filepath.Join("glibc-hwcaps", "x86-64-v3", "libgdk-3.so.0")
				}
				path := filepath.Join(value.directory, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					test.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("unverified alternative"), 0644); err != nil {
					test.Fatal(err)
				}
			case "directory symlink":
				if err := os.Symlink(test.TempDir(), filepath.Join(value.directory, "glibc-hwcaps")); err != nil {
					test.Fatal(err)
				}
			}
			value.metadata.GTKModules["colorreload"] = component
			value.write(test)
			if selected, err := plan(value.config); err == nil || selected != nil {
				test.Fatalf("unverified module selection accepted: %v, %v", selected, err)
			}
		})
	}
}

func TestActivationReexecutesOnceAndPreservesEnvironment(test *testing.T) {
	value := runtimeFixture(test)
	value.config.ResolveGTK = func(variables map[string]string) (string, error) {
		if variables[loaderVariable] != "/custom directory" {
			test.Fatalf("compatibility resolution included private GTK: %s", variables[loaderVariable])
		}
		return value.gtk, nil
	}
	previous := append([]string(nil), value.config.Environment...)
	first, err := plan(value.config)
	if err != nil || first == nil {
		test.Fatalf("activation plan = %v, %v", first, err)
	}
	variables := environmentValues(first.Environment)
	if variables[loaderVariable] != value.directory+":/custom directory" || variables[activeVariable] != value.directory || variables["UNRELATED"] != "value" {
		test.Fatalf("unexpected activation environment: %v", variables)
	}
	if !reflect.DeepEqual(value.config.Environment, previous) {
		test.Fatal("the input environment changed")
	}
	value.config.Environment = first.Environment
	second, err := plan(value.config)
	if err != nil || second != nil {
		test.Fatalf("activation repeated: %v, %v", second, err)
	}
}

func TestSystemSelectionRemovesOnlyManagedLoaderDirectory(test *testing.T) {
	value := runtimeFixture(test)
	value.config.Environment = []string{
		modeVariable + "=system", activeVariable + "=" + value.directory,
		loaderVariable + "=" + value.directory + ":/custom::/other", "UNRELATED=value",
	}
	selected, err := plan(value.config)
	if err != nil || selected == nil {
		test.Fatalf("system selection = %v, %v", selected, err)
	}
	variables := environmentValues(selected.Environment)
	if _, present := variables[activeVariable]; present || variables[loaderVariable] != "/custom::/other" || variables["UNRELATED"] != "value" {
		test.Fatalf("system selection changed user settings: %v", variables)
	}
	value.config.Environment = selected.Environment
	if selected, err := plan(value.config); err != nil || selected != nil {
		test.Fatalf("system selection repeated: %v, %v", selected, err)
	}
}

func TestExplicitLoaderInterpositionAndSystemModeRemainAvailable(test *testing.T) {
	for _, entry := range []string{"LD_PRELOAD=custom.so", "LD_AUDIT=custom.so", modeVariable + "=system"} {
		test.Run(entry, func(test *testing.T) {
			value := runtimeFixture(test)
			value.config.Environment = append(value.config.Environment, entry)
			if selected, err := plan(value.config); err != nil || selected != nil {
				test.Fatalf("explicit override was changed: %v, %v", selected, err)
			}
		})
	}
}

func TestDynamicLoaderPathRemainsUnderUserControl(test *testing.T) {
	for _, path := range []string{"$ORIGIN/lib", "${LIB}/custom", "/first;/second"} {
		value := runtimeFixture(test)
		value.config.Environment = []string{loaderVariable + "=" + path}
		if selected, err := plan(value.config); err != nil || selected != nil {
			test.Fatalf("dynamic loader path changed: %v, %v", selected, err)
		}
		value.config.Environment = append(value.config.Environment, directoryVariable+"="+value.directory)
		if selected, err := plan(value.config); err == nil || selected != nil {
			test.Fatalf("explicit incompatible loader path accepted: %v, %v", selected, err)
		}
	}
}

func TestHardwareSpecificGTKSelectionIsRejected(test *testing.T) {
	directory := test.TempDir()
	if err := rejectHardwareGTK(directory); err != nil {
		test.Fatal(err)
	}
	variant := filepath.Join(directory, "glibc-hwcaps", "x86-64-v3")
	if err := os.MkdirAll(variant, 0755); err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(variant, "libgtk-3.so.0"), []byte("another GTK"), 0644); err != nil {
		test.Fatal(err)
	}
	if err := rejectHardwareGTK(directory); err == nil {
		test.Fatal("an ambiguous hardware-specific GTK was accepted")
	}
}

func TestRuntimeDirectoryCannotBecomeLoaderSyntax(test *testing.T) {
	for _, character := range []string{":", ";", "$"} {
		value := runtimeFixture(test)
		parent := filepath.Join(test.TempDir(), "build"+character+"suffix")
		if err := os.MkdirAll(parent, 0755); err != nil {
			test.Fatal(err)
		}
		directory := filepath.Join(parent, "native-runtime")
		if err := os.Rename(value.directory, directory); err != nil {
			test.Fatal(err)
		}
		value.config.Executable = filepath.Join(parent, "mygoframe")
		if selected, err := plan(value.config); err == nil || selected != nil {
			test.Fatalf("unsafe default runtime path accepted: %v, %v", selected, err)
		}
		value.config.Environment = append(value.config.Environment, directoryVariable+"="+directory)
		if selected, err := plan(value.config); err == nil || selected != nil {
			test.Fatalf("unsafe explicit runtime path accepted: %v, %v", selected, err)
		}
	}
}

func TestUnsafeOrIncompatibleRuntimeIsRejected(test *testing.T) {
	cases := map[string]func(*testing.T, *fixture){
		"schema":       func(_ *testing.T, value *fixture) { value.metadata.SchemaVersion = 2 },
		"architecture": func(_ *testing.T, value *fixture) { value.metadata.Architecture = "arm64" },
		"multiarch":    func(_ *testing.T, value *fixture) { value.metadata.Multiarch = "aarch64-linux-gnu" },
		"library hash": func(_ *testing.T, value *fixture) {
			component := value.metadata.Libraries["gdk"]
			component.SHA256 = strings.Repeat("0", 64)
			value.metadata.Libraries["gdk"] = component
		},
		"parent traversal": func(_ *testing.T, value *fixture) {
			component := value.metadata.Libraries["gdk"]
			component.File = "../libgtk-3.so.0"
			value.metadata.Libraries["gdk"] = component
		},
		"escaping symlink": func(test *testing.T, value *fixture) {
			component := value.metadata.Libraries["gdk"]
			path := filepath.Join(value.directory, component.File)
			if err := os.Remove(path); err != nil {
				test.Fatal(err)
			}
			if err := os.Symlink(value.gtk, path); err != nil {
				test.Fatal(err)
			}
		},
		"wrong soname": func(_ *testing.T, value *fixture) {
			component := value.metadata.Libraries["gdk"]
			component.SONAME = "libwrong.so.0"
			value.metadata.Libraries["gdk"] = component
		},
		"unexpected library": func(test *testing.T, value *fixture) {
			if err := os.WriteFile(filepath.Join(value.directory, "libepoxy.so.0"), []byte("unexpected"), 0644); err != nil {
				test.Fatal(err)
			}
		},
		"system GTK hash": func(_ *testing.T, value *fixture) {
			component := value.metadata.SystemCompatibility["gtk"]
			component.SHA256 = strings.Repeat("0", 64)
			value.metadata.SystemCompatibility["gtk"] = component
		},
		"reexec loop": func(_ *testing.T, value *fixture) {
			value.config.Environment = append(value.config.Environment, activeVariable+"="+value.directory)
		},
		"mode typo": func(_ *testing.T, value *fixture) {
			value.config.Environment = append(value.config.Environment, modeVariable+"=typo")
		},
		"empty override": func(_ *testing.T, value *fixture) {
			value.config.Environment = append(value.config.Environment, directoryVariable+"=")
		},
	}
	for name, mutate := range cases {
		test.Run(name, func(test *testing.T) {
			value := runtimeFixture(test)
			mutate(test, &value)
			value.write(test)
			if selected, err := plan(value.config); err == nil || selected != nil {
				test.Fatalf("unsafe runtime was accepted: %v, %v", selected, err)
			}
		})
	}
}

func TestMissingDefaultBundleDoesNotLoadNativeLibraries(test *testing.T) {
	config := configuration{
		Executable: filepath.Join(test.TempDir(), "bin", "mygoframe"), Architecture: "amd64",
		ResolveGTK: func(map[string]string) (string, error) {
			test.Fatal("native resolution ran without a bundle")
			return "", nil
		},
	}
	if selected, err := plan(config); selected != nil || err != nil {
		test.Fatalf("missing optional runtime = %v, %v", selected, err)
	}
	config.Environment = []string{directoryVariable + "=" + filepath.Join(test.TempDir(), "missing")}
	if selected, err := plan(config); selected != nil || err == nil {
		test.Fatalf("missing explicit runtime was accepted: %v, %v", selected, err)
	}
}

func TestMalformedAndOversizedManifestAreRejected(test *testing.T) {
	for _, contents := range []string{"{", strings.Repeat(" ", maximumManifest+1)} {
		value := runtimeFixture(test)
		if err := os.WriteFile(filepath.Join(value.directory, "manifest.json"), []byte(contents), 0644); err != nil {
			test.Fatal(err)
		}
		if selected, err := plan(value.config); selected != nil || err == nil {
			test.Fatalf("invalid manifest was accepted: %v, %v", selected, err)
		}
	}
}

func TestGTKCacheSelectionUsesNativeArchitecture(test *testing.T) {
	cache := `libgtk-3.so.0 (libc6,AArch64) => /arm/libgtk-3.so.0
libgtk-3.so.0 (libc6,x86-64) => /amd/libgtk-3.so.0
libgtk-3.so.0.extra (libc6,x86-64) => /wrong/libgtk-3.so.0`
	for architecture, expected := range map[string]string{"amd64": "/amd/libgtk-3.so.0", "arm64": "/arm/libgtk-3.so.0", "386": ""} {
		if actual := gtkFromCache(cache, architecture); actual != expected {
			test.Fatalf("%s selected %q, expected %q", architecture, actual, expected)
		}
	}
}

func TestUserGTKLoaderPathIsResolvedFirst(test *testing.T) {
	directory := test.TempDir()
	path := filepath.Join(directory, "libgtk-3.so.0")
	if err := os.WriteFile(path, []byte("user GTK"), 0644); err != nil {
		test.Fatal(err)
	}
	actual, err := resolveSystemGTK(map[string]string{loaderVariable: directory})
	if err != nil || actual != path {
		test.Fatalf("user GTK resolution = %q, %v", actual, err)
	}
}

func TestEmptyLoaderComponentHonorsCurrentDirectory(test *testing.T) {
	directory := test.TempDir()
	test.Chdir(directory)
	if err := os.WriteFile("libgtk-3.so.0", []byte("current directory GTK"), 0644); err != nil {
		test.Fatal(err)
	}
	actual, err := resolveSystemGTK(map[string]string{loaderVariable: ":/unused"})
	if err != nil || actual != "libgtk-3.so.0" {
		test.Fatalf("current-directory GTK resolution = %q, %v", actual, err)
	}
}

func TestEventDrivenPaintCapabilityFollowsVerifiedRuntime(test *testing.T) {
	value := runtimeFixture(test)
	first, err := plan(value.config)
	if err != nil || first == nil || environmentValues(first.Environment)[eventPaintVariable] != "" {
		test.Fatalf("an older runtime enabled opaque event-driven paints: %v, %v", first, err)
	}
	value.metadata.Features = map[string]bool{"eventDrivenPaint": true}
	value.write(test)
	value.config.Environment = first.Environment
	selected, err := plan(value.config)
	if err != nil || selected == nil || environmentValues(selected.Environment)[eventPaintVariable] != "1" {
		test.Fatalf("verified event-driven paint activation failed: %v, %v", selected, err)
	}
	value.config.Environment = selected.Environment
	if repeated, err := plan(value.config); err != nil || repeated != nil {
		test.Fatalf("event-driven paints caused repeated execution: %v, %v", repeated, err)
	}
	value.config.Environment = replaceVariable(selected.Environment, eventPaintVariable, "0", false)
	overridden, err := plan(value.config)
	if err != nil || overridden == nil || environmentValues(overridden.Environment)[eventPaintVariable] != "0" || environmentValues(overridden.Environment)[eventPaintActiveVariable] != "" {
		test.Fatalf("an inherited capability overrode the caller's selection: %v, %v", overridden, err)
	}
	value.config.Environment = selected.Environment
	value.metadata.Features = nil
	value.write(test)
	retired, err := plan(value.config)
	if err != nil || retired == nil || environmentValues(retired.Environment)[eventPaintVariable] != "" {
		test.Fatalf("runtime downgrade retained its paint capability: %v, %v", retired, err)
	}
	value.config.Environment = append(selected.Environment, modeVariable+"=system")
	system, err := plan(value.config)
	if err != nil || system == nil || environmentValues(system.Environment)[eventPaintVariable] != "" || environmentValues(system.Environment)[eventPaintActiveVariable] != "" {
		test.Fatalf("system selection retained managed event-driven paints: %v, %v", system, err)
	}
}
