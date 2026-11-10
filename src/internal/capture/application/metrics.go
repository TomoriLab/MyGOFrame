package application

// GPURenderMetrics records capture-to-submission timing for one renderer update.
type GPURenderMetrics struct {
	ContextID           uint64 `json:"contextID"`
	Sequence            uint64 `json:"sequence"`
	CaptureTimestampNS  uint64 `json:"captureTimestampNanoseconds"`
	SubmitTimestampNS   uint64 `json:"submitTimestampNanoseconds"`
	SubmissionCPUTimeNS uint64 `json:"submissionCPUTimeNanoseconds"`
	Width               uint32 `json:"width"`
	Height              uint32 `json:"height"`
}
