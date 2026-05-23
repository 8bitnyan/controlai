package main

// codec_adapters.go wires the thin codec.go dispatch layer to the concrete
// cbor and json library calls, keeping codec.go free of library imports so it
// is easy to unit-test with stubs.

import (
	"encoding/json"

	cbor "github.com/fxamacker/cbor/v2"
)

func cborUnmarshal(data []byte, v any) error {
	return cbor.Unmarshal(data, v)
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
