package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/domain/branding"
)

func main() {
	if err := install(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func install() error {
	executable, err := filepath.Abs("build/mygoframe")
	if err != nil {
		return err
	}
	information, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("locate built application: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("application is not an executable file: %s", executable)
	}
	if strings.Contains(executable, "=") {
		return fmt.Errorf("desktop entries do not support executable paths containing '=': %s", executable)
	}
	artwork, err := os.ReadFile("src/internal/desktop/presentation/assets/logo.png")
	if err != nil {
		return fmt.Errorf("read application icon: %w", err)
	}
	icon, err := branding.RoundedPNG(artwork, 512)
	if err != nil {
		return err
	}
	dataDirectory := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataDirectory) {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate user data directory: %w", err)
		}
		dataDirectory = filepath.Join(homeDirectory, ".local", "share")
	}
	iconPath := filepath.Join(dataDirectory, "mygoframe", "icon.png")
	entryPath := filepath.Join(dataDirectory, "applications", "mygoframe.desktop")
	entry := "[Desktop Entry]\nType=Application\nName=MyGOFrame\n" +
		"Comment=Low-latency capture-card viewer\nExec=" + executableValue(executable) +
		"\nIcon=" + desktopValue(iconPath) +
		"\nTerminal=false\nCategories=AudioVideo;Video;\nStartupWMClass=mygoframe\n"
	if err := installFile(iconPath, icon); err != nil {
		return err
	}
	if err := installFile(entryPath, []byte(entry)); err != nil {
		return err
	}
	fmt.Printf("Installed desktop entry: %s\nInstalled icon: %s\n", entryPath, iconPath)
	return nil
}

func executableValue(path string) string {
	// Desktop string escaping is applied after command-line quoting, because launchers undo it first.
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`, "%", "%%").Replace(path)
	return desktopValue(`"` + quoted + `"`)
}

func desktopValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`, " ", `\s`).Replace(value)
}

func installFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mygoframe-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(0o644); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
