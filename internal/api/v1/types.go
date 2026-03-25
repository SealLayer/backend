package v1

type SealRequest struct {
	ContentHash string `json:"content_hash"`
}

type SealResponse struct {
	Receipt any `json:"receipt,omitempty"`

	BatchID   string `json:"batch_id,omitempty"`
	StatusURL string `json:"status_url,omitempty"`

	Error string `json:"error,omitempty"`
}

type StatusResponse struct {
	BatchID    string `json:"batch_id"`
	Status     string `json:"status"`
	LedgerPath string `json:"ledger_path,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Error      string `json:"error,omitempty"`
}
