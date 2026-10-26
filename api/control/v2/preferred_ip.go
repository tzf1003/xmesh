package v2

// PreferredIPState is the Cloudflare preferred-address pool as the kernel sees it: what is in it, where each
// address came from, how it has been doing, and what the scheduler is up to.
type PreferredIPState struct {
	Enabled       bool                     `json:"enabled"`
	Mode          string                   `json:"mode"`
	NoDNSFallback bool                     `json:"no_dns_fallback"`
	Pool          []PreferredIPEntry       `json:"pool"`
	Sources       []PreferredIPSourceState `json:"sources"`
	Scan          PreferredIPScanState     `json:"scan"`
	Peers         []PreferredIPPeerState   `json:"peers"`
}

// PreferredIPEntry is one pooled address. Status is ok, untested or cooling.
type PreferredIPEntry struct {
	IP     string `json:"ip"`
	Source string `json:"source"`
	Status string `json:"status"`
	// LatencyMS and LossPercent come from the last scan or the live connections; null until measured.
	LatencyMS     *int  `json:"latency_ms"`
	LossPercent   *int  `json:"loss_percent"`
	Failures      int   `json:"failures"`
	CooldownUntil *Time `json:"cooldown_until"`
	LastUsedAt    *Time `json:"last_used_at"`
}

// PreferredIPSourceState reports one configured source. Index is its position in
// mesh.cloudflare.preferred_ip.sources, or -1 for the older preferred_ips and preferred_ips_file fields.
type PreferredIPSourceState struct {
	Index     int    `json:"index"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Disabled  bool   `json:"disabled"`
	Count     int    `json:"count"`
	FetchedAt *Time  `json:"fetched_at"`
	Error     string `json:"error"`
}

// PreferredIPScanState is the scheduled speed test.
type PreferredIPScanState struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Sample    int    `json:"sample"`
	Host      string `json:"host"`
	StartedAt *Time  `json:"started_at"`
	LastAt    *Time  `json:"last_at"`
	NextAt    *Time  `json:"next_at"`
	Kept      int    `json:"kept"`
	Error     string `json:"error"`
}

// PreferredIPPeerState is how one configured peer is dialed. Mode is smart, fixed or off; Current is the
// address its last connection used, empty when it used the DNS address or has not connected yet.
type PreferredIPPeerState struct {
	DeviceID string   `json:"device_id"`
	Hostname string   `json:"hostname"`
	Mode     string   `json:"mode"`
	Override bool     `json:"override"`
	IPs      []string `json:"ips"`
	Current  string   `json:"current"`
	LastAt   *Time    `json:"last_at"`
}

// PreferredIPRunRequest starts work now: "scan" runs the speed test in the background, "refresh" fetches
// every subscription source and returns when done.
type PreferredIPRunRequest struct {
	Task string `json:"task"`
}

// PreferredIPInspectRequest checks pasted text (one address per line, or a CloudflareSpeedTest result.csv)
// without changing anything.
type PreferredIPInspectRequest struct {
	Text string `json:"text"`
}

// PreferredIPInspectItem is one line of the pasted text. Status is new, existing or rejected.
type PreferredIPInspectItem struct {
	Line   int    `json:"line"`
	IP     string `json:"ip"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

type PreferredIPInspectResponse struct {
	Items    []PreferredIPInspectItem `json:"items"`
	Accepted []string                 `json:"accepted"`
}
