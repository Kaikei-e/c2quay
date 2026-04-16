package doctor

import (
	"encoding/json"
	"io"

	"github.com/Kaikei-e/c2quay/internal/output"
)

// Render emits the report to the given writer in the given format.
func Render(w io.Writer, r Report, format string) error {
	if format == "json" {
		return renderJSON(w, r)
	}
	renderText(w, r)
	return nil
}

func renderText(w io.Writer, r Report) {
	tw := output.NewText(w)
	for _, res := range r.Results {
		if res.OK {
			tw.Ok(res.Name, res.Detail)
		} else {
			tw.Fail(res.Name, res.Detail)
		}
	}
}

type jsonEntry struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type jsonReport struct {
	OK     bool        `json:"ok"`
	Checks []jsonEntry `json:"checks"`
}

func renderJSON(w io.Writer, r Report) error {
	out := jsonReport{OK: r.AllOK()}
	for _, res := range r.Results {
		out.Checks = append(out.Checks, jsonEntry{Name: res.Name, OK: res.OK, Detail: res.Detail})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
