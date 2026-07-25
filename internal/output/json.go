package output

import (
	"encoding/json"
	"io"
)

// Report is the machine-readable output schema for verify/deploy.
type Report struct {
	Env      string          `json:"env"`
	Command  string          `json:"command"`
	Results  []ServiceResult `json:"results"`
	Summary  ReportSummary   `json:"summary"`
	AuditURL string          `json:"audit_url,omitempty"`
}

type ServiceResult struct {
	Service     string `json:"service"`
	Pacticipant string `json:"pacticipant"`
	Version     string `json:"version"`
	ImageRef    string `json:"image_ref,omitempty"`
	Verdict     string `json:"verdict"`
	Reason      string `json:"reason,omitempty"`
	BrokerURL   string `json:"broker_url,omitempty"`
}

type ReportSummary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
}

func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
