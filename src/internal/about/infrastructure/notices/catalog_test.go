package notices

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestEmbeddedNoticesAreCompleteAndOffline(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Developer != "zeroday0619" || catalog.Organization != "Tomori Lab" || catalog.Contact != "zero@tomori.dev" || catalog.Version == "" {
		t.Fatal("application metadata is missing")
	}
	required := map[string]bool{
		"mygoframe": false, "go:github.com/egoist/mygo": false,
		"go:github.com/ebitengine/purego": false, "go:github.com/go-text/typesetting": false,
		"go:golang.org/x/image": false, "go:golang.org/x/text": false,
		"runtime:go": false, "runtime:swift": false, "runtime:rust": false,
		"runtime:foundation": false, "runtime:dispatch": false, "runtime:icu": false,
		"native:gtk": false, "native:fcitx": false, "native:kde": false,
	}
	seen := make(map[string]bool)
	for _, component := range catalog.Components {
		if component.ID == "" || seen[component.ID] || component.Name == "" || component.Scope == "" || component.License == "" || len(component.Documents) == 0 {
			t.Fatalf("invalid or duplicate component: %+v", component)
		}
		seen[component.ID] = true
		if _, exists := required[component.ID]; exists {
			required[component.ID] = true
		}
		for _, identifier := range component.Documents {
			document, exists := catalog.Documents[identifier]
			digest, digestError := hex.DecodeString(document.SHA256)
			if !exists || document.Title == "" || document.Source == "" || strings.TrimSpace(document.Text) == "" || digestError != nil || len(digest) != 32 {
				t.Fatalf("incomplete document %q for %s", identifier, component.ID)
			}
		}
		body := catalog.LicenseText(component)
		if component.ID == "mygoframe" && !strings.Contains(body, "Permission is hereby granted, free of charge") {
			t.Fatal("the full application MIT license is missing")
		}
		if component.ID == "native:kde" && !strings.Contains(body, "GNU GENERAL PUBLIC LICENSE") {
			t.Fatal("the KDE GPL text is missing")
		}
	}
	for identifier, found := range required {
		if !found {
			t.Errorf("required component %s has no embedded notices", identifier)
		}
	}
}
