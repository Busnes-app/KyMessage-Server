package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func (s *Server) handleMessagingUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	usage, err := s.store.Messaging().Usage(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Messaging storage usage unavailable")
		return
	}
	type counts struct {
		ActiveEvents  int64 `json:"active_events"`
		RetainedBytes int64 `json:"retained_bytes"`
		Receipts      int64 `json:"receipts"`
	}
	type room struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Retired bool   `json:"retired"`
		counts
	}
	rooms := make([]room, 0, len(usage.LargestRooms))
	for _, entry := range usage.LargestRooms {
		rooms = append(rooms, room{entry.ID, entry.Name, entry.Retired, counts{entry.ActiveEvents, entry.RetainedBytes, entry.Receipts}})
	}
	s.writeJSON(w, http.StatusOK, struct {
		RoomCount int64  `json:"room_count"`
		Totals    counts `json:"totals"`
		Limits    counts `json:"limits"`
		Rooms     []room `json:"rooms"`
		SampledAt string `json:"sampled_at"`
	}{usage.Rooms, counts{usage.ActiveEvents, usage.RetainedBytes, usage.Receipts},
		counts{store.MessagingActiveEventLimit, store.MessagingRetainedByteLimit, store.MessagingReceiptLimit}, rooms, time.Now().UTC().Format(time.RFC3339)})
}
