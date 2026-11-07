package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TomoriLab/MyGOFrame/internal/branding"
)

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	source := flag.String("source", "cmd/mygoframe/assets/logo.png", "Read the application artwork from this PNG file.")
	output := flag.String("output", "", "Write the rounded 512-pixel application icon to this path.")
	flag.Parse()
	if *output == "" || flag.NArg() != 0 {
		return fmt.Errorf("provide --output with a destination path and no positional arguments")
	}
	artwork, err := os.ReadFile(*source)
	if err != nil {
		return fmt.Errorf("read application artwork: %w", err)
	}
	icon, err := branding.RoundedPNG(artwork, 512)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return fmt.Errorf("create icon output directory: %w", err)
	}
	if err := os.WriteFile(*output, icon, 0o644); err != nil {
		return fmt.Errorf("write application icon: %w", err)
	}
	return nil
}
