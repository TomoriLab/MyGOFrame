package nativeruntime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// Prepare activates private libraries before any native UI resource is created.
// A startup re-execution is required because glibc caches its loader search path.
// Native owner cleanup and sanitizer exit checks still run in the final process.
func Prepare() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	config := configuration{
		Executable: executable, Environment: os.Environ(), Architecture: runtime.GOARCH,
		ResolveGTK: resolveSystemGTK,
	}
	launch, err := plan(config)
	if err != nil || launch == nil {
		return err
	}
	if err := syscall.Exec(executable, os.Args, launch.Environment); err != nil {
		return fmt.Errorf("activate private native runtime: %w", err)
	}
	return nil
}

func resolveSystemGTK(values map[string]string) (string, error) {
	if values[loaderVariable] != "" {
		for _, directory := range strings.Split(values[loaderVariable], ":") {
			if directory == "" {
				directory = "."
			}
			if err := rejectHardwareGTK(directory); err != nil {
				return "", err
			}
			path := filepath.Join(directory, "libgtk-3.so.0")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path, nil
			}
		}
	}
	for _, command := range []string{"/usr/sbin/ldconfig", "/sbin/ldconfig"} {
		if _, err := os.Stat(command); err != nil {
			continue
		}
		// The read-only cache query follows configured nonstandard library paths.
		process := exec.Command(command, "-p")
		process.Env = replaceVariable(os.Environ(), "LC_ALL", "C", false)
		output, err := process.Output()
		if err != nil {
			return "", fmt.Errorf("read native library cache: %w", err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "libgtk-3.so.0 (") && strings.Contains(line, "/glibc-hwcaps/") {
				return "", fmt.Errorf("hardware-specific GTK cache entries require %s=system", modeVariable)
			}
		}
		if path := gtkFromCache(string(output), runtime.GOARCH); path != "" {
			if err := rejectHardwareGTK(filepath.Dir(path)); err != nil {
				return "", err
			}
			return path, nil
		}
		break
	}
	for _, directory := range []string{"/usr/lib/" + multiarch(runtime.GOARCH), "/lib/" + multiarch(runtime.GOARCH), "/usr/lib64", "/lib64", "/usr/lib", "/lib"} {
		if err := rejectHardwareGTK(directory); err != nil {
			return "", err
		}
		path := filepath.Join(directory, "libgtk-3.so.0")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", fmt.Errorf("libgtk-3.so.0 was not found for %s", runtime.GOARCH)
}

func rejectHardwareGTK(directory string) error {
	// The loader chooses these variants from CPU capabilities rather than path order.
	// Refusing an ambiguous selection prevents checking a different GTK file.
	paths, err := filepath.Glob(filepath.Join(directory, "glibc-hwcaps", "*", "libgtk-3.so.0"))
	if err != nil || len(paths) != 0 {
		return fmt.Errorf("hardware-specific GTK libraries require %s=system", modeVariable)
	}
	return nil
}

func gtkFromCache(output, architecture string) string {
	tag := map[string]string{"amd64": "x86-64", "arm64": "aarch64"}[architecture]
	if tag == "" {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		left, path, found := strings.Cut(strings.TrimSpace(line), " => ")
		if !found || !strings.HasPrefix(left, "libgtk-3.so.0 (") || !strings.Contains(strings.ToLower(left), tag) {
			continue
		}
		if filepath.IsAbs(path) {
			return path
		}
	}
	return ""
}
