package reports

// Health is the node's own vitals, sent with a report. A zero field means the
// node could not read it (unsupported platform), not zero usage.
type Health struct {
	// Load1 is the one-minute load average.
	Load1 float64 `json:"l1,omitempty"`
	// MemUsed and MemTotal are bytes.
	MemUsed  int64 `json:"mu,omitempty"`
	MemTotal int64 `json:"mt,omitempty"`
	// DiskFree is bytes free on the node's data filesystem.
	DiskFree int64 `json:"df,omitempty"`
	// Uptime is seconds since boot.
	Uptime int64 `json:"up,omitempty"`
	// Version is the agent's build version, when known.
	Version string `json:"v,omitempty"`
}
