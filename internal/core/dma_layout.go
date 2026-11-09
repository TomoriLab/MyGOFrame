package core

// validDMALayout checks the native descriptor before a frame can reach the renderer.
func validDMALayout(fourcc uint32, width, height, stride, uvOffset, byteLength uint64, encoding uint32) bool {
	const maximumBytes = 128 * 1024 * 1024
	if width == 0 || height == 0 || width > 16384 || height > 16384 ||
		width*height*4 > maximumBytes || stride > 0x7fffffff || byteLength > maximumBytes {
		return false
	}
	var bytesPerPixel uint64
	planar, rgb := false, false
	switch fourcc {
	case 0x3231564e: // NV12 stores separate luma and interleaved chroma planes.
		bytesPerPixel, planar = 1, true
	case 0x30313050: // P010 stores little-endian 16-bit words with ten significant bits.
		bytesPerPixel, planar = 2, true
		if stride%2 != 0 {
			return false
		}
	case 0x56595559, 0x59565955: // YUYV and UYVY pack two pixels into four bytes.
		bytesPerPixel = 2
	case 0x33524742, 0x33424752: // BGR3 and RGB3 store three bytes per pixel.
		bytesPerPixel, rgb = 3, true
	default:
		return false
	}
	if stride < width*bytesPerPixel || (!rgb && (width%2 != 0 || (encoding != 1 && encoding != 2))) ||
		(rgb && encoding != 0) || (planar && height%2 != 0) {
		return false
	}
	firstPlane := stride * height
	if planar {
		return uvOffset == firstPlane && byteLength >= firstPlane+stride*(height/2)
	}
	return uvOffset == 0 && byteLength >= firstPlane
}
