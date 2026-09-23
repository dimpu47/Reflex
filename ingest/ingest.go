package ingest

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// Alert represents an incoming infrastructure alert.
type Alert struct {
	ID          string          `json:"id"`
	ServiceName string          `json:"service_name"`
	Payload     json.RawMessage `json:"payload"`
}

// Handler handles incoming HTTP webhooks and puts them on the channel.
type Handler struct {
	AlertChan chan<- Alert
	Wg        *sync.WaitGroup
}

// ServeHTTP parses the webhook payload and forwards it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var alert Alert
	if err := json.Unmarshal(body, &alert); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Track the pending alert in the WaitGroup so we can shut down gracefully
	h.Wg.Add(1)
	h.AlertChan <- alert

	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"accepted"}`))
}
