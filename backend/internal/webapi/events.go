package webapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Event struct {
	ID     int64  `json:"id"`
	Type   string `json:"type"`
	Data   any    `json:"data"`
	UserID string `json:"-"`
}

type EventHub struct {
	mu      sync.Mutex
	nextID  int64
	clients map[chan Event]string
}

func NewEventHub() *EventHub {
	return &EventHub{clients: map[chan Event]string{}}
}

func (h *EventHub) Publish(userID, typ string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	ev := Event{ID: h.nextID, Type: typ, Data: data, UserID: userID}
	for ch, uid := range h.clients {
		if uid == userID || uid == "*" {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (h *EventHub) subscribe(userID string) chan Event {
	ch := make(chan Event, 64)
	h.mu.Lock()
	h.clients[ch] = userID
	h.mu.Unlock()
	return ch
}

func (h *EventHub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "服务器不支持事件流")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch := s.Hub.subscribe(u.ID)
	defer s.Hub.unsubscribe(ch)
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			b, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, b)
			flusher.Flush()
		case <-keep.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
