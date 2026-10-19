package v2

import (
	"encoding/json"
	"fmt"
	"time"
)

// timeLayout is the Timestamp format of the schemas: RFC 3339, UTC, fixed milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Time is a time.Time that always encodes as UTC with millisecond precision.
type Time struct{ time.Time }

// Now returns the current time, truncated to milliseconds.
func Now() Time { return Time{time.Now().UTC().Truncate(time.Millisecond)} }

// At wraps t, converted to UTC and truncated to milliseconds.
func At(t time.Time) Time { return Time{t.UTC().Truncate(time.Millisecond)} }

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(timeLayout))
}

func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.Parse(timeLayout, s)
	if err != nil {
		return fmt.Errorf("control: timestamp %q is not %s", s, timeLayout)
	}
	t.Time = parsed
	return nil
}
