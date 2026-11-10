package presentation

import (
	"os"
	"path/filepath"
	"strings"
)

func themePreferencePath() (string, error) {
	directory, err := os.UserConfigDir()
	return filepath.Join(directory, "mygoframe", "theme"), err
}

func loadThemePreference() (bool, bool) {
	path, err := themePreferencePath()
	if err != nil {
		return false, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(string(data)) {
	case "dark":
		return true, true
	case "light":
		return false, true
	default:
		return false, false
	}
}

func saveThemePreference(dark bool) error {
	path, err := themePreferencePath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".theme-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	value := "light\n"
	if dark {
		value = "dark\n"
	}
	if _, err = file.WriteString(value); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
