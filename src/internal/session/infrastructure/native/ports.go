package native

import (
	capture "github.com/TomoriLab/MyGOFrame/src/internal/capture/application"
	session "github.com/TomoriLab/MyGOFrame/src/internal/session/application"
)

var _ session.Engine = (*Engine)(nil)
var _ capture.Frame = (*Frame)(nil)
var _ capture.GPUImage = (*GPUImage)(nil)
