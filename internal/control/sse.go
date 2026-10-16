package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

const heartbeat = 15 * time.Second

// events streams Server-Sent Events. A client that sends Last-Event-ID receives
// the retained events after it first; if that id is older than the ring, it gets
// a lifecycle "gap" event and should refetch state.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, 500, toolerr.New("internal", "连接不支持流式响应", "op", "control.events"))
		return
	}
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.URL.Query().Get("last_event_id")
	}
	var since uint64
	if last != "" {
		var err error
		if since, err = strconv.ParseUint(last, 10, 64); err != nil {
			fail(w, 400, toolerr.Args("Last-Event-ID"))
			return
		}
	}
	past, gap, sub := s.bus.Subscribe(since, last != "")
	defer sub.Close()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if gap {
		writeEvent(w, Event{Type: "lifecycle", Time: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Data: map[string]any{"state": "gap"}})
	}
	for _, e := range past {
		writeEvent(w, e)
	}
	flusher.Flush()
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-sub.C:
			if !open {
				return
			}
			writeEvent(w, e)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
func writeEvent(w http.ResponseWriter, e Event) {
	raw, _ := json.Marshal(e)
	if e.ID != 0 {
		fmt.Fprintf(w, "id: %d\n", e.ID)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, raw)
}
