package v1

import "github.com/tzf1003/xmesh/internal/computer"

type ComputerState = computer.LocalState

type ComputerPauseRequest struct {
	Paused bool `json:"paused"`
}

type ComputerRevokeRequest struct {
	SessionID string `json:"session_id"`
}
