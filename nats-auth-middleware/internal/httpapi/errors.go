package httpapi

import (
	"encoding/json"
	"net/http"
)

// writeError matches the error body shape
// docs/nats-tenant-queue-api/design.md §6 fixes for the whole system:
// {"error": "<short_code>", "message": "<human readable>"}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
