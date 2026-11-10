package main

import (
	"fmt"
	"os"

	"github.com/TomoriLab/MyGOFrame/src/internal/bootstrap"
)

func main() {
	if err := bootstrap.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
