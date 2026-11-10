package presentation

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/png"
	"sync"

	"github.com/TomoriLab/MyGOFrame/src/internal/desktop/domain/branding"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

//go:embed assets/logo.png
var applicationIcon []byte

type brandingImages struct {
	logo       *ui.Bitmap
	windowIcon []byte
}

// The original artwork is embedded so branding does not depend on the working directory.
var applicationBranding = sync.OnceValue(func() brandingImages {
	artwork, err := png.Decode(bytes.NewReader(applicationIcon))
	if err != nil {
		panic(fmt.Sprintf("Decode embedded application logo: %v", err))
	}
	// GTK silently discards oversized X11 icons, so the native representation is bounded.
	icon, err := branding.RoundedPNG(applicationIcon, 256)
	if err != nil {
		panic(fmt.Sprintf("Encode application window icon: %v", err))
	}
	return brandingImages{logo: ui.NewBitmap(artwork), windowIcon: icon}
})

func (application *applicationState) applyWindowIcon(window *mygo.Window) {
	if err := window.SetIcon(applicationBranding().windowIcon); err != nil {
		application.errorMessage = fmt.Sprintf("Set window icon: %v", err)
	}
}
