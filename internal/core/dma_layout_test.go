package core

import "testing"

func TestDMALayoutBoundaries(t *testing.T) {
	for _, test := range []struct {
		format                                string
		width, height, stride, offset, length uint64
		encoding                              uint32
	}{
		{"NV12", 4, 2, 8, 16, 24, 2},
		{"P010", 4, 2, 12, 24, 36, 1},
		{"YUYV", 4, 3, 12, 0, 36, 2},
		{"UYVY", 4, 3, 12, 0, 36, 1},
		{"BGR3", 3, 3, 11, 0, 33, 0},
		{"RGB3", 3, 3, 11, 0, 33, 0},
	} {
		t.Run(test.format, func(t *testing.T) {
			fourcc, _ := FourCC(test.format)
			if !validDMALayout(fourcc, test.width, test.height, test.stride, test.offset, test.length, test.encoding) {
				t.Fatal("valid padded layout was rejected")
			}
			for _, malformed := range []struct {
				width, height, stride, offset, length uint64
				encoding                              uint32
			}{
				{test.width, test.height, test.stride, test.offset, test.length - 1, test.encoding},
				{test.width, test.height, test.stride, test.offset + 1, test.length, test.encoding},
				{test.width, test.height, test.stride, test.offset, test.length, 7},
				{test.width, test.height, 1, test.offset, test.length, test.encoding},
				{test.width, test.height, ^uint64(0), test.offset, test.length, test.encoding},
				{^uint64(0), test.height, test.stride, test.offset, test.length, test.encoding},
				{0, test.height, test.stride, test.offset, test.length, test.encoding},
				{test.width, 0, test.stride, test.offset, test.length, test.encoding},
				{test.width, test.height, test.stride, test.offset, 128*1024*1024 + 1, test.encoding},
			} {
				if validDMALayout(fourcc, malformed.width, malformed.height, malformed.stride, malformed.offset, malformed.length, malformed.encoding) {
					t.Errorf("accepted malformed layout: %+v", malformed)
				}
			}
		})
	}
	for _, format := range []string{"MJPG", "NM12", "XXXX"} {
		fourcc, _ := FourCC(format)
		if validDMALayout(fourcc, 4, 2, 8, 0, 16, 2) {
			t.Errorf("accepted %s", format)
		}
	}
	if validDMALayout(0x30313050, 4, 2, 9, 18, 27, 2) {
		t.Fatal("accepted odd P010 stride")
	}
	if validDMALayout(0x3231564e, 4, 3, 4, 12, 18, 2) {
		t.Fatal("accepted odd NV12 height")
	}
	if validDMALayout(0x56595559, 3, 2, 8, 0, 16, 2) {
		t.Fatal("accepted odd YUYV width")
	}
}
