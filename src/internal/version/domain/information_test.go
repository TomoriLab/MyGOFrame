package domain

import "testing"

func TestVersionSummaryUsesAvailableProvenance(t *testing.T) {
	for _, sample := range []struct {
		information Information
		expected    string
	}{
		{Information{Version: "1.2.3"}, "MyGOFrame 1.2.3"},
		{Information{Version: "1.2.3", Revision: "0123456789abcdef", Modified: true}, "MyGOFrame 1.2.3 (0123456789ab, modified)"},
		{Information{Version: "1.2.3", Revision: "0123456789abcdef"}, "MyGOFrame 1.2.3 (0123456789ab)"},
	} {
		if value := sample.information.String(); value != sample.expected {
			t.Fatalf("%q, want %q", value, sample.expected)
		}
	}
}
