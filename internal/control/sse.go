package control

import (
	"fmt"
	"net/http"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// events streams Server-Sent Events (CTR-CTL-04). A first connection gets hello with the current
// head. A reconnect with Last-Event-ID gets the retained events after it, or a reset when the id
// belongs to another boot or fell out of the ring.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		fail(w, newError(v1.ErrInternal, "连接不支持流式响应"))
		return
	}
	since := r.Header.Get("Last-Event-ID")
	if since == "" {
		since = r.URL.Query().Get("last_event_id")
	}
	join := s.bus.Subscribe(since)
	defer join.Sub.Close()
	h := w.Header()
	h.Set("Content-Type", v1.ContentTypeSSE)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(e v1.Event) bool {
		raw, err := e.SSE()
		if err != nil {
			return false
		}
		_, err = w.Write(raw)
		return err == nil
	}
	hello, _ := v1.NewEvent("", 0, v1.Now(), v1.EventHello, v1.HelloData{BootID: s.bus.BootID(), Head: join.Head, HeartbeatS: v1.HeartbeatSeconds})
	if !send(hello) {
		return
	}
	if join.Reset != nil {
		reset, _ := v1.NewEvent("", 0, v1.Now(), v1.EventReset, join.Reset)
		if !send(reset) {
			return
		}
	}
	for _, e := range join.Replay {
		if !send(e) {
			return
		}
	}
	flusher.Flush()
	tick := time.NewTicker(s.o.heartbeat())
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-join.Sub.C:
			if !open {
				return
			}
			if !send(e) {
				return
			}
			flusher.Flush()
		case <-tick.C:
			if _, err := fmt.Fprint(w, v1.Heartbeat); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
