package domain

import "testing"

func TestFourCC(t *testing.T) {
	value, err := FourCC("NV12")
	if err != nil || value != 0x3231564e {
		t.Fatalf("FourCC: %x, %v", value, err)
	}
	for _, invalid := range []string{"", "RGB", "NV123", "\x00V12"} {
		if _, err := FourCC(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
