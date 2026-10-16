package schemas

import _ "embed"

// ControlRoutes is the control channel route table (CTR-CTL-12).
//
//go:embed control-routes.json
var ControlRoutes []byte
