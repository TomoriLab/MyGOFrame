package domain

// Information identifies the release and source inputs without discovering runtime state.
type Information struct {
	Version         string `json:"version"`
	DebianVersion   string `json:"debianVersion"`
	Revision        string `json:"revision,omitempty"`
	Modified        bool   `json:"modified"`
	SourceDateEpoch int64  `json:"sourceDateEpoch,omitempty"`
}

func (information Information) String() string {
	result := "MyGOFrame " + information.Version
	details := information.Revision
	if len(details) > 12 {
		details = details[:12]
	}
	if information.Modified {
		if details != "" {
			details += ", "
		}
		details += "modified"
	}
	if details != "" {
		result += " (" + details + ")"
	}
	return result
}
