// Package v2 holds the Go types of the local control channel, API version 2.
//
// The JSON Schemas under docs/contracts/schemas (control-*.schema.json) are the only source of the
// contract (CTR-CTL-12). The types here are hand written to match them, and the tests in this
// package fail when they drift: every fixture under docs/contracts/schemas/examples/control must
// decode into these types without unknown fields and encode back to the same JSON.
//
// Conventions that keep the encoded form schema-valid:
//
//   - Fields the schema marks required are never omitted. A nullable value is a pointer without
//     omitempty, so nil encodes as null.
//   - Required arrays must be non-nil before encoding, otherwise they encode as null. Use
//     make([]T, 0) for an empty list.
//   - Timestamps use Time, which always encodes as UTC with three fractional digits.
//   - Nothing here carries command output, file content, terminal data, environment values or
//     tool arguments. Adding such a field to an event or a view breaks INV-10 and the fixtures.
package v2
