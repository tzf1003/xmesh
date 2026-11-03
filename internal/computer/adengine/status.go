package adengine

import "slices"

// Engine states reported to people and to Info.engine_status.
const (
	StateNative              = "native"
	StateNotInstalled        = "not_installed"
	StateIntegrityFailed     = "integrity_failed"
	StateVersionUnsupported  = "version_unsupported"
	StateUpdateAvailable     = "update_available"
	StateUpstreamUnvalidated = "upstream_unvalidated"
	StateCurrent             = "current"
)

// Status is the device-side view of the agent-desktop engine. Effective is the
// engine the Host runs: agent-desktop only in the states that pass verification
// and the version gate, and native otherwise.
type Status struct {
	Platform       string `json:"platform"`
	Configured     string `json:"configured"`
	Effective      string `json:"effective"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	Pinned         string `json:"pinned"`
	Installed      string `json:"installed,omitempty"`
	InstalledFrom  string `json:"installed_source,omitempty"`
	Path           string `json:"path,omitempty"`
	UpstreamLatest string `json:"upstream_latest,omitempty"`
	// Validated reports whether the installed version (or the pin, when nothing is
	// installed) is accepted on this platform.
	Validated        bool   `json:"validated"`
	ValidationRecord string `json:"validation_record,omitempty"`
}

// Evaluate decides the state of the engine from the manifest, the platform and
// the verdict on the executable the Host would run.
func Evaluate(m *Manifest, key, configured string, id Identity) Status {
	st := Status{
		Platform:         key,
		Configured:       configured,
		Effective:        EngineNative,
		Pinned:           m.Version,
		Installed:        id.Version,
		InstalledFrom:    id.Source,
		Path:             id.Path,
		UpstreamLatest:   m.UpstreamLatest(),
		ValidationRecord: m.Validation.Record,
	}
	checked := m.Version
	if id.Version != "" {
		checked = id.Version
	}
	st.Validated = m.Validated(checked, key)
	if configured != EngineAgentDesktop {
		st.State = StateNative
		return st
	}
	switch {
	case id.Problem == ProblemNotInstalled || (id.Problem == "" && !id.Found):
		st.State, st.Reason = StateNotInstalled, ProblemNotInstalled
		return st
	case id.Problem != "":
		// Includes a pointer that cannot be read: the install cannot be verified.
		st.State, st.Reason = StateIntegrityFailed, id.Problem
		return st
	case !m.Validated(id.Version, key):
		st.State, st.Reason = StateVersionUnsupported, unsupportedReason(m, id.Version, key)
		return st
	}
	cur, err := ParseVersion(id.Version)
	if err != nil {
		st.State, st.Reason = StateIntegrityFailed, ProblemReceipt
		return st
	}
	st.Effective = EngineAgentDesktop
	pinned, _ := ParseVersion(m.Version)
	upstream := m.UpstreamLatest()
	switch {
	case pinned.Compare(cur) > 0 && m.Validated(m.Version, key):
		st.State, st.Reason = StateUpdateAvailable, "pinned_newer_validated"
	case pinned.Compare(cur) > 0:
		st.State, st.Reason = StateUpstreamUnvalidated, "pinned_not_validated"
	case upstream != "" && newer(upstream, cur) && !m.Validated(upstream, key):
		st.State, st.Reason = StateUpstreamUnvalidated, "upstream_not_validated"
	default:
		st.State = StateCurrent
	}
	return st
}

func newer(v string, cur Version) bool {
	parsed, err := ParseVersion(v)
	return err == nil && parsed.Compare(cur) > 0
}

func unsupportedReason(m *Manifest, version, key string) string {
	if !slices.Contains(m.Validation.ValidatedVersions, version) {
		return "version_not_validated"
	}
	return "platform_not_validated"
}

// Usable reports whether a state runs the agent-desktop engine.
func Usable(state string) bool {
	return state == StateCurrent || state == StateUpdateAvailable || state == StateUpstreamUnvalidated
}
