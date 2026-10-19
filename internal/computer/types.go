// Package computer owns remote desktop sessions and the input authority of an Agent.
package computer

import (
	"context"
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"io"
	"time"
)

const ProtocolVersion = 1

type Display struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	X        int     `json:"x"`
	Y        int     `json:"y"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Scale    float64 `json:"scale"`
	Rotation int     `json:"rotation"`
}

type Info struct {
	ProtocolVersion int       `json:"protocol_version"`
	Available       bool      `json:"available"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	DesktopID       string    `json:"desktop_id"`
	Epoch           string    `json:"desktop_epoch"`
	LayoutVersion   string    `json:"layout_version"`
	Capabilities    []string  `json:"capabilities"`
	Displays        []Display `json:"displays"`
}

type Region struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type CaptureRequest struct {
	DisplayID string  `json:"display_id,omitempty"`
	MaxWidth  int     `json:"max_width,omitempty"`
	Region    *Region `json:"region,omitempty"`
}

type Frame struct {
	ID              string    `json:"frame_id"`
	Epoch           string    `json:"desktop_epoch"`
	DisplayID       string    `json:"display_id"`
	LayoutVersion   string    `json:"layout_version"`
	CapturedAt      time.Time `json:"captured_at"`
	Width           int       `json:"image_width"`
	Height          int       `json:"image_height"`
	Region          Region    `json:"capture_region"`
	CoordinateSpace string    `json:"coordinate_space"`
	MIMEType        string    `json:"mime_type"`
	Image           []byte    `json:"-"`
}

type Action struct {
	Type       string   `json:"type"`
	X          *int     `json:"x,omitempty"`
	Y          *int     `json:"y,omitempty"`
	ToX        *int     `json:"to_x,omitempty"`
	ToY        *int     `json:"to_y,omitempty"`
	Button     string   `json:"button,omitempty"`
	Clicks     int      `json:"clicks,omitempty"`
	DeltaX     int      `json:"delta_x,omitempty"`
	DeltaY     int      `json:"delta_y,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Text       string   `json:"text,omitempty"`
	DurationMS int      `json:"duration_ms,omitempty"`
}

type OpenRequest struct {
	DesktopID  string `json:"desktop_id,omitempty"`
	Mode       string `json:"mode"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type Session struct {
	ID        string    `json:"session_id"`
	DesktopID string    `json:"desktop_id"`
	Epoch     string    `json:"desktop_epoch"`
	Mode      string    `json:"mode"`
	Token     string    `json:"lease_token,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Owner     string    `json:"owner"`
}

type SessionRequest struct {
	SessionID string `json:"session_id"`
	Epoch     string `json:"desktop_epoch"`
	Token     string `json:"lease_token,omitempty"`
}

type ObserveRequest struct {
	SessionRequest
	CaptureRequest
}

type RenewRequest struct {
	SessionRequest
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type ActRequest struct {
	SessionRequest
	ActionID     string   `json:"action_id"`
	FrameID      string   `json:"frame_id,omitempty"`
	Actions      []Action `json:"actions"`
	ObserveAfter bool     `json:"observe_after,omitempty"`
}

type StatusRequest struct {
	SessionRequest
	ActionID string `json:"action_id"`
}

type ActionResult struct {
	ActionID string `json:"action_id"`
	State    string `json:"state"`
	Executed int    `json:"executed"`
	Error    string `json:"error,omitempty"`
	Frame    *Frame `json:"frame,omitempty"`
}

// Execution carries the authority and exact observation that an adapter must validate again.
type Execution struct {
	Epoch    string   `json:"desktop_epoch"`
	Token    string   `json:"lease_token"`
	ActionID string   `json:"action_id"`
	Frame    *Frame   `json:"frame,omitempty"`
	Actions  []Action `json:"actions"`
}

// Provider is the authenticated connection to the user-session desktop executor.
// Execute releases held input before returning; Release also interrupts a running action.
// An error after dispatch must be represented by State=unknown unless the adapter proves no input occurred.
type Provider interface {
	Info(context.Context) (Info, error)
	Capture(context.Context, CaptureRequest) (Frame, error)
	Execute(context.Context, Execution) (ActionResult, error)
	Release(context.Context, Execution) error
	Close() error
}

type Settings struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint,omitempty"`
	MaxSessions   int    `json:"max_sessions,omitempty"`
	LeaseSeconds  int    `json:"lease_seconds,omitempty"`
	MaxImageBytes int    `json:"max_image_bytes,omitempty"`
}

func (s Settings) Effective() Settings {
	if s.MaxSessions == 0 {
		s.MaxSessions = 16
	}
	if s.LeaseSeconds == 0 {
		s.LeaseSeconds = 120
	}
	if s.MaxImageBytes == 0 {
		s.MaxImageBytes = 4 << 20
	}
	return s
}

// Decode rejects nested unknown fields as well as top-level spelling errors.
func Decode(raw json.RawMessage, dst any) error {
	d := json.NewDecoder(bytesReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return toolerr.Args("arguments")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return toolerr.Args("arguments")
	}
	return nil
}
