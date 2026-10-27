package request

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func Body[T any](w http.ResponseWriter, r *http.Request) (result T, err error) {
	var value *T
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&value); err != nil {
		return result, fmt.Errorf("invalid request body: %w", err)
	}
	if value == nil {
		return result, fmt.Errorf("request body must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return result, fmt.Errorf("request body must contain one JSON object")
	}
	return *value, nil
}
