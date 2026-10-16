package control

// ListenOptions selects where the channel listens and who may connect.
type ListenOptions struct {
	StateDir string
	Node     string
	// AllowSIDs adds Windows principals to the pipe's access list, next to the
	// kernel's own user, SYSTEM and Administrators. install --system uses it to
	// let the installing user's UI reach a service that runs as SYSTEM.
	AllowSIDs []string
}
