package winbox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestSupervisorProtocol(t *testing.T) {
	valid := supervisorRequest{Sequence: 2, Operation: "retain", Job: 1, Handle: 42}
	var framed bytes.Buffer
	writer := &shortSupervisorWriter{writer: &framed, limit: 3}
	if err := writeSupervisorFrame(writer, valid); err != nil {
		t.Fatal(err)
	}
	var decoded supervisorRequest
	if err := readSupervisorFrame(&framed, &decoded); err != nil || !reflect.DeepEqual(valid, decoded) {
		t.Fatal(decoded, err)
	}
	for _, body := range []string{`{"sequence":2,"operation":"close","extra":true}`, `{"sequence":2,"operation":"close"} {}`, `null false`, `{`} {
		t.Run(body, func(t *testing.T) {
			var b bytes.Buffer
			binary.Write(&b, binary.LittleEndian, uint32(len(body)))
			b.WriteString(body)
			if err := readSupervisorFrame(&b, new(supervisorRequest)); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
	}
	for _, size := range []uint32{0, maxSupervisorFrame + 1} {
		var b bytes.Buffer
		binary.Write(&b, binary.LittleEndian, size)
		if err := readSupervisorFrame(&b, new(supervisorRequest)); err == nil {
			t.Fatal("size accepted", size)
		}
	}
	var complete bytes.Buffer
	if err := writeSupervisorFrame(&complete, valid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < complete.Len(); i++ {
		if err := readSupervisorFrame(bytes.NewReader(complete.Bytes()[:i]), new(supervisorRequest)); err == nil {
			t.Fatal("torn frame accepted", i)
		}
	}
	if err := writeSupervisorFrame(&shortSupervisorWriter{writer: io.Discard}, valid); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if err := writeSupervisorFrame(io.Discard, strings.Repeat("x", maxSupervisorFrame)); err == nil {
		t.Fatal("oversize write accepted")
	}
	for _, r := range []supervisorRequest{
		{}, {Sequence: maxJournalRecords + 1, Operation: "close"}, {Sequence: 2, Operation: "unknown"},
		{Sequence: 2, Operation: "new-job", Job: 1}, {Sequence: 2, Operation: "reap", Handle: 1},
		{Sequence: 2, Operation: "retain", Job: 1}, {Sequence: 2, Operation: "close", Init: &supervisorInit{}},
		{Sequence: 2, Operation: "init", Init: &supervisorInit{}},
	} {
		if err := r.validate(); err == nil {
			t.Fatal("invalid payload accepted", r)
		}
	}
	object := journalObject{ID: [16]byte{1}}
	init := &supervisorInit{Limit: 2, Intent: 2, ExecutionID: strings.Repeat("a", 32), Journal: supervisorGuard{Handle: 1, Object: object}, Guards: []supervisorGuard{{Handle: 2, Object: journalObject{ID: [16]byte{2}, Directory: true}}}}
	if err := (supervisorRequest{Sequence: 1, Operation: "init", Init: init}).validate(); err != nil {
		t.Fatal(err)
	}
	init.Guards = append(init.Guards, init.Guards[0])
	if err := (supervisorRequest{Sequence: 1, Operation: "init", Init: init}).validate(); err == nil {
		t.Fatal("aliased bootstrap accepted")
	}
}

type shortSupervisorWriter struct {
	writer io.Writer
	limit  int
}

func (w *shortSupervisorWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit {
		b = b[:w.limit]
	}
	return w.writer.Write(b)
}
