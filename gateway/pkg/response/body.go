package response

import (
	"encoding/json"
	"net/http"
)

func Body[T any](w http.ResponseWriter, obj T, status int) error {
	json, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(json)
	return err
}
