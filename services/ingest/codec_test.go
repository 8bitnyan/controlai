package main

import (
	"encoding/json"
	"testing"

	cbor "github.com/fxamacker/cbor/v2"
)

// TestDecodePayload_CBOR_success verifies that a valid CBOR map is decoded to JSON.
func TestDecodePayload_CBOR_success(t *testing.T) {
	input := map[string]any{"temp": 22.5, "unit": "C"}
	raw, err := cbor.Marshal(input)
	if err != nil {
		t.Fatalf("marshal CBOR: %v", err)
	}

	res := decodePayload("cbor", raw)

	if res.DecodeErr != nil {
		t.Fatalf("unexpected decode error: %v", res.DecodeErr)
	}
	if !res.IsJSON {
		t.Fatal("expected IsJSON=true for valid CBOR")
	}
	if len(res.JSONPayload) == 0 {
		t.Fatal("expected non-empty JSONPayload")
	}
	// Validate the JSON round-trip.
	var out map[string]any
	if err := json.Unmarshal(res.JSONPayload, &out); err != nil {
		t.Fatalf("JSONPayload is not valid JSON: %v", err)
	}
	if out["unit"] != "C" {
		t.Errorf("expected unit=C, got %v", out["unit"])
	}
	// Raw must be the original bytes.
	if string(res.Raw) != string(raw) {
		t.Error("Raw bytes differ from original payload")
	}
}

// TestDecodePayload_CBOR_failureIsNonFatal verifies that a CBOR decode failure
// stores raw bytes and sets DecodeErr but does not panic.
func TestDecodePayload_CBOR_failureIsNonFatal(t *testing.T) {
	bad := []byte{0xff, 0xfe, 0x00, 0x01} // invalid CBOR

	res := decodePayload("cbor", bad)

	if res.DecodeErr == nil {
		t.Fatal("expected decode error for invalid CBOR")
	}
	if res.IsJSON {
		t.Fatal("IsJSON must be false after decode failure")
	}
	if len(res.JSONPayload) != 0 {
		t.Fatal("JSONPayload must be nil after decode failure")
	}
	if string(res.Raw) != string(bad) {
		t.Error("Raw bytes must be preserved even after decode failure")
	}
}

// TestDecodePayload_JSON_success verifies that valid JSON is forwarded as-is.
func TestDecodePayload_JSON_success(t *testing.T) {
	raw := []byte(`{"sensor":"pm2.5","value":34}`)

	res := decodePayload("json", raw)

	if res.DecodeErr != nil {
		t.Fatalf("unexpected decode error: %v", res.DecodeErr)
	}
	if !res.IsJSON {
		t.Fatal("expected IsJSON=true for valid JSON")
	}
	if string(res.JSONPayload) != string(raw) {
		t.Errorf("JSONPayload mismatch: want %s got %s", raw, res.JSONPayload)
	}
}

// TestDecodePayload_JSON_failureIsNonFatal verifies that JSON parse failure is non-fatal.
func TestDecodePayload_JSON_failureIsNonFatal(t *testing.T) {
	bad := []byte(`not json`)

	res := decodePayload("json", bad)

	if res.DecodeErr == nil {
		t.Fatal("expected decode error for invalid JSON")
	}
	if res.IsJSON {
		t.Fatal("IsJSON must be false after decode failure")
	}
	if string(res.Raw) != string(bad) {
		t.Error("Raw bytes must be preserved even after decode failure")
	}
}

// TestDecodePayload_RawPassthrough verifies that raw_passthrough never decodes.
func TestDecodePayload_RawPassthrough(t *testing.T) {
	raw := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	res := decodePayload("raw_passthrough", raw)

	if res.DecodeErr != nil {
		t.Fatalf("raw_passthrough must never return a decode error, got: %v", res.DecodeErr)
	}
	if res.IsJSON {
		t.Fatal("IsJSON must be false for raw_passthrough")
	}
	if len(res.JSONPayload) != 0 {
		t.Fatal("JSONPayload must be nil for raw_passthrough")
	}
	if string(res.Raw) != string(raw) {
		t.Error("Raw bytes differ from original payload in raw_passthrough mode")
	}
}

// TestDecodePayload_UnknownCodecTreatedAsPassthrough verifies that an unknown codec
// stores raw bytes without panicking (fail-safe behaviour).
func TestDecodePayload_UnknownCodecTreatedAsPassthrough(t *testing.T) {
	raw := []byte("binary-data")
	res := decodePayload("unknown-codec", raw)
	if res.IsJSON {
		t.Error("unknown codec must not set IsJSON")
	}
	if string(res.Raw) != string(raw) {
		t.Error("Raw bytes must be preserved for unknown codec")
	}
}
