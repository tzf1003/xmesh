package ci

// Inhibitor keeps the machine awake while jobs run. Acquire returns the function that lets it
// sleep again; it is safe to call the release more than once.
type Inhibitor func() (release func())
