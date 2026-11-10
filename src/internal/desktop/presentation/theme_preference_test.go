package presentation

import "testing"

func TestThemePreferenceSurvivesReload(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, configured := loadThemePreference(); configured {
		t.Fatal("missing preference overrides system")
	}
	for _, dark := range []bool{true, false} {
		if err := saveThemePreference(dark); err != nil {
			t.Fatal(err)
		}
		restored, configured := loadThemePreference()
		if !configured || restored != dark {
			t.Fatal("theme preference was not restored")
		}
	}
}
