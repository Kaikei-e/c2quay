package composeadapter

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ParsePsJSON handles both shapes that `docker compose ps --format json` can
// emit: a JSON array (older Compose) or a stream of newline-separated JSON
// objects (newer Compose).
func ParsePsJSON(raw []byte) ([]ContainerStatus, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '[' {
		var arr []ContainerStatus
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("decode compose ps json array: %w", err)
		}
		return arr, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out []ContainerStatus
	for dec.More() {
		var s ContainerStatus
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("decode compose ps ndjson: %w", err)
		}
		out = append(out, s)
	}
	return out, nil
}
