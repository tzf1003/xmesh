package v2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// FormatEventID builds the <boot_id>.<seq> id of a buffered event.
func FormatEventID(bootID string, seq uint64) string {
	return bootID + "." + strconv.FormatUint(seq, 10)
}

// ParseEventID splits an id into boot id and sequence number.
func ParseEventID(id string) (bootID string, seq uint64, err error) {
	boot, num, ok := strings.Cut(id, ".")
	if !ok || len(boot) != 8 {
		return "", 0, fmt.Errorf("control: malformed event id %q", id)
	}
	seq, err = strconv.ParseUint(num, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("control: malformed event id %q", id)
	}
	return boot, seq, nil
}

// Event is one stream event. ID and Seq are set on buffered events and empty on hello and reset,
// which are never replayed. Data holds the payload for Type; use Payload to decode it.
type Event struct {
	ID   string          `json:"id,omitempty"`
	Seq  uint64          `json:"seq,omitempty"`
	Time Time            `json:"time"`
	Type EventType       `json:"type"`
	Data json.RawMessage `json:"data"`
}

type HelloData struct {
	BootID     string  `json:"boot_id"`
	Head       *string `json:"head"`
	HeartbeatS int     `json:"heartbeat_s"`
}

type ResetData struct {
	Reason string  `json:"reason"`
	Head   *string `json:"head"`
}

type PeerEventData struct {
	Change string `json:"change"`
	Peer   Peer   `json:"peer"`
}

type CallEventData struct {
	Tool        string  `json:"tool"`
	Device      string  `json:"device"`
	Caller      string  `json:"caller"`
	OAuthClient *string `json:"oauth_client"`
	Status      string  `json:"status"`
	Error       *string `json:"error"`
	DurationMS  int64   `json:"duration_ms"`
}

type SessionEventData struct {
	Change  string  `json:"change"`
	Session Session `json:"session"`
}

type WarningEventData struct {
	Change  string  `json:"change"`
	Warning Warning `json:"warning"`
}

type ConfigEventData struct {
	Change          string   `json:"change"`
	Actor           Actor    `json:"actor"`
	ETag            *string  `json:"etag"`
	RestartRequired bool     `json:"restart_required"`
	ChangedPaths    []string `json:"changed_paths"`
}

type LifecycleEventData struct {
	Change string     `json:"change"`
	Drain  DrainState `json:"drain"`
}

// NewEvent builds an event with the given payload. id and seq are zero for hello and reset.
func NewEvent(id string, seq uint64, t Time, typ EventType, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, Seq: seq, Time: t, Type: typ, Data: raw}, nil
}

// Payload decodes Data into the struct for e.Type (a pointer), rejecting unknown fields.
func (e Event) Payload() (any, error) {
	var v any
	switch e.Type {
	case EventHello:
		v = new(HelloData)
	case EventReset:
		v = new(ResetData)
	case EventStatus:
		v = new(StatusResponse)
	case EventPeer:
		v = new(PeerEventData)
	case EventCall:
		v = new(CallEventData)
	case EventSession:
		v = new(SessionEventData)
	case EventWarning:
		v = new(WarningEventData)
	case EventConfig:
		v = new(ConfigEventData)
	case EventLifecycle:
		v = new(LifecycleEventData)
	default:
		return nil, fmt.Errorf("control: unknown event type %q", e.Type)
	}
	if err := strictUnmarshal(e.Data, v); err != nil {
		return nil, err
	}
	return v, nil
}

// SSE encodes the event as one server-sent event: id (when buffered), event, data, blank line.
func (e Event) SSE() ([]byte, error) {
	line, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if e.ID != "" {
		b.WriteString("id: " + e.ID + "\n")
	}
	b.WriteString("event: " + string(e.Type) + "\n")
	b.WriteString("data: ")
	b.Write(line)
	b.WriteString("\n\n")
	return b.Bytes(), nil
}
