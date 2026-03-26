package v1

type SealRequest struct {
	ContentHash string `json:"content_hash"`
}

type SealResponse struct {
	Receipt any `json:"receipt,omitempty"`

	BatchID   string `json:"batch_id,omitempty"`
	StatusURL string `json:"status_url,omitempty"`

	Error      string `json:"error,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type StatusResponse struct {
	BatchID    string `json:"batch_id"`
	Status     string `json:"status"`
	LedgerPath string `json:"ledger_path,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Error      string `json:"error,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type PublicConfigResponse struct {
	ProtocolVersion      string `json:"protocol_version"`
	PublicKeyFingerprint string `json:"public_key_fingerprint"`
	StatusPollIntervalMs int64  `json:"status_poll_interval_ms"`
	BatchIntervalMs      int64  `json:"batch_interval_ms"`
	ServerTimeUTC        string `json:"server_time_utc"`
}
