package main

// codecResult holds the outcome of decoding an MQTT payload.
type codecResult struct {
	// JSONPayload is the JSON-encoded decoded value for the payload jsonb column.
	// Nil when decoding was skipped or failed.
	JSONPayload []byte
	// Raw is the original MQTT payload bytes (always set).
	Raw []byte
	// IsJSON is true when JSONPayload is valid and should be stored in the JSONB column.
	IsJSON bool
	// DecodeErr holds the decode error when IsJSON is false due to a failure.
	// A nil DecodeErr with IsJSON=false means raw_passthrough mode.
	DecodeErr error
}

// decodePayload dispatches the MQTT payload through the configured codec.
// codec must be one of "cbor", "json", or "raw_passthrough".
// On decode failure the result's IsJSON is false and DecodeErr is set, but Raw
// is always populated — the caller should persist raw bytes even on failure.
func decodePayload(codec string, raw []byte) codecResult {
	res := codecResult{Raw: raw}
	switch codec {
	case "cbor":
		var decoded map[string]any
		if err := cborUnmarshal(raw, &decoded); err != nil {
			res.DecodeErr = err
			return res
		}
		b, err := jsonMarshal(decoded)
		if err != nil {
			res.DecodeErr = err
			return res
		}
		res.JSONPayload = b
		res.IsJSON = true
	case "json":
		var decoded map[string]any
		if err := jsonUnmarshal(raw, &decoded); err != nil {
			res.DecodeErr = err
			return res
		}
		res.JSONPayload = raw
		res.IsJSON = true
	case "raw_passthrough":
		// No decoding — raw bytea only.
	}
	return res
}
