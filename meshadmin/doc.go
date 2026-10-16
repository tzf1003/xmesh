// Package meshadmin is the control-plane administration client behind
// `xmesh mesh`. It speaks to the official Tailscale API (v2) and to
// Headscale (REST /api/v1) through one Provider interface, so tag inspection,
// policy writes, auth-key creation, DERP configuration and the permission
// matrix behave the same on both. See docs/contracts/meshadmin.md.
//
// The package is deliberately outside internal/: the Electron shell and a
// future server console reuse it through the `xmesh mesh --json` commands.
// Provider differences live here and nowhere else (RUL-20); the kernel and the
// gateway never branch on the control plane (CON-NET-04).
package meshadmin
