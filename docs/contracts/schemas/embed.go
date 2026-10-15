// Package schemas exposes the authoritative contract schemas to the runtime.
package schemas

import "embed"

//go:embed *.schema.json
var Files embed.FS
