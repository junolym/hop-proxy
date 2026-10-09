package harness

import (
	"encoding/json"
	"io"
)

// EncodeJSON marshals v to JSON bytes.
func EncodeJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// decodeJSON is a package-internal alias used by wait.go.
func decodeJSON(r io.Reader, v interface{}) error {
	return DecodeJSON(r, v)
}
