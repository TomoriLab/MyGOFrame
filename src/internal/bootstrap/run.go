package bootstrap

import (
	"fmt"
	"os"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/presentation"
	graphicsruntime "github.com/TomoriLab/MyGOFrame/src/internal/platform/infrastructure/graphics"
	nativeruntime "github.com/TomoriLab/MyGOFrame/src/internal/platform/infrastructure/native"
)

// Run is the only production composition root for native services and startup policy.
func Run() error { return presentation.Run(services(), prepareDesktop) }
func prepareDesktop(measureLatency bool) error {
	if measureLatency {
		if err := os.Setenv("MYGOFRAME_MEASURE_LATENCY", "1"); err != nil {
			return err
		}
	}
	if err := nativeruntime.Prepare(); err != nil {
		return err
	}
	if _, err := graphicsruntime.Prepare(); err != nil {
		fmt.Fprintf(os.Stderr, "Graphics compatibility preparation failed; retaining the normal driver configuration: %v\n", err)
	}
	if os.Getenv("MYGO_GL_API") == "" {
		return os.Setenv("MYGO_GL_API", "gles")
	}
	return nil
}
