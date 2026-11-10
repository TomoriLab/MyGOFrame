package domain

import "time"

// Event carries diagnostic text without retaining media or native resource handles.
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

type State struct {
	Enabled    bool   `json:"enabled"`
	EventCount int    `json:"eventCount"`
	Dropped    uint64 `json:"dropped"`
	Error      string `json:"error,omitempty"`
}

type Snapshot struct {
	State  State   `json:"state"`
	Events []Event `json:"events"`
}
