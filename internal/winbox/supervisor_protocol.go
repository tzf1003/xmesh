package winbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

const maxSupervisorFrame = 2 << 20

type supervisorGuard struct {
	Handle uint64        `json:"handle"`
	Object journalObject `json:"object"`
}

type supervisorInit struct {
	Limit       int               `json:"limit"`
	Intent      uint64            `json:"intent"`
	ExecutionID string            `json:"execution_id"`
	Journal     supervisorGuard   `json:"journal"`
	Guards      []supervisorGuard `json:"guards"`
}

type supervisorRequest struct {
	Sequence  uint64          `json:"sequence"`
	Operation string          `json:"operation"`
	Init      *supervisorInit `json:"init,omitempty"`
	Job       uint32          `json:"job,omitempty"`
	Handle    uint64          `json:"handle,omitempty"`
	ExitCode  uint32          `json:"exit_code,omitempty"`
}

type supervisorReply struct {
	Sequence uint64 `json:"sequence"`
	Job      uint32 `json:"job,omitempty"`
	Handle   uint64 `json:"handle,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r supervisorRequest) validate() error {
	if r.Sequence == 0 || r.Sequence > maxJournalRecords {
		return fmt.Errorf("invalid supervisor sequence")
	}
	if r.Operation != "init" && r.Init != nil {
		return fmt.Errorf("unexpected supervisor bootstrap")
	}
	switch r.Operation {
	case "init":
		if r.Sequence != 1 || r.Init == nil || r.Job != 0 || r.Handle != 0 || r.ExitCode != 0 {
			break
		}
		i := r.Init
		if i.Limit < 1 || i.Limit > 256 || i.Intent == 0 || !journalExecutionID.MatchString(i.ExecutionID) || i.Journal.Handle == 0 || !validJournalObject(i.Journal.Object) || i.Journal.Object.Directory || len(i.Guards) == 0 || len(i.Guards) > 16384 {
			break
		}
		handles := map[uint64]bool{i.Journal.Handle: true}
		for _, guard := range i.Guards {
			if guard.Handle == 0 || handles[guard.Handle] || !validJournalObject(guard.Object) || !guard.Object.Directory {
				return fmt.Errorf("invalid supervisor namespace guard")
			}
			handles[guard.Handle] = true
		}
		return nil
	case "new-job", "close":
		if r.Job == 0 && r.Handle == 0 && r.ExitCode == 0 {
			return nil
		}
	case "retain":
		if r.Job > 0 && r.Handle > 0 && r.ExitCode == 0 {
			return nil
		}
	case "reap":
		if r.Job == 0 && r.Handle == 0 {
			return nil
		}
	}
	return fmt.Errorf("invalid supervisor operation payload")
}

func writeSupervisorFrame(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) == 0 || len(body) > maxSupervisorFrame {
		return fmt.Errorf("supervisor frame size limit")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
	for _, part := range [][]byte{header[:], body} {
		for len(part) != 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 || n > len(part) {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

func readSupervisorFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > maxSupervisorFrame {
		return fmt.Errorf("supervisor frame size limit")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("supervisor frame trailing data")
	}
	return nil
}
