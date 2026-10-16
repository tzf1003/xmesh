package winbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

// costJournal returns a journal holding a profile and a workspace of count
// objects, ready for one grant per object.
func costJournal(tb testing.TB, count int) (*grantJournal, []journalObject) {
	j, _, objects := costJournalAt(tb, count)
	return j, objects
}

func costJournalAt(tb testing.TB, count int) (*grantJournal, string, []journalObject) {
	tb.Helper()
	j, path := journalFixtureTB(tb)
	if _, err := j.append(journalProfile()); err != nil {
		tb.Fatal(err)
	}
	objects := make([]journalObject, count)
	for i := range objects {
		objects[i] = journalObject{Volume: 17, ID: [16]byte{byte(i), byte(i >> 8), byte(i >> 16), 1}, Directory: i == 0}
	}
	workspace := &journalWorkspace{Path: `C:\work`, Root: objects[0], Objects: objects}
	if _, err := j.append(journalRecord{Kind: "workspace", Workspace: workspace}); err != nil {
		tb.Fatal(err)
	}
	return j, path, objects
}

func costGrant(object journalObject) journalRecord {
	ace := make([]byte, 16)
	binary.LittleEndian.PutUint16(ace[2:4], 16)
	return journalRecord{Kind: "grant", Grant: &journalGrant{Object: object, ACE: ace}}
}

// BenchmarkGrantJournalAppend measures the journal part of admitting and then
// releasing a workspace of N objects, one record per object.
func BenchmarkGrantJournalAppend(b *testing.B) {
	for _, count := range []int{100, 1000, 4000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				j, objects := costJournal(b, count)
				b.StartTimer()
				var intents []uint64
				for _, object := range objects {
					sequence, err := j.append(costGrant(object))
					if err != nil {
						b.Fatal(err)
					}
					intents = append(intents, sequence)
				}
				for _, sequence := range intents {
					if _, err := j.append(journalRecord{Kind: "released", GrantSequence: sequence}); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

func grantBatch(objects []journalObject) []journalRecord {
	records := make([]journalRecord, len(objects))
	for i, object := range objects {
		records[i] = costGrant(object)
	}
	return records
}

// A batch is a flush optimization only: the file must be byte-for-byte what the
// same records appended one at a time would have produced.
func TestGrantJournalBatchMatchesSeparateAppends(t *testing.T) {
	const count = 50
	single, singlePath, objects := costJournalAt(t, count)
	batched, batchedPath, _ := costJournalAt(t, count)
	var intents []uint64
	for _, object := range objects {
		sequence, err := single.append(costGrant(object))
		if err != nil {
			t.Fatal(err)
		}
		intents = append(intents, sequence)
	}
	sequences, err := batched.appendBatch(grantBatch(objects))
	if err != nil || len(sequences) != count {
		t.Fatal("grant batch", sequences, err)
	}
	for i := range intents {
		if sequences[i] != intents[i] {
			t.Fatalf("sequence %d: batch %d separate %d", i, sequences[i], intents[i])
		}
	}
	releases := make([]journalRecord, count)
	for i := count - 1; i >= 0; i-- {
		if _, err := single.append(journalRecord{Kind: "released", GrantSequence: intents[i]}); err != nil {
			t.Fatal(err)
		}
		releases[count-1-i] = journalRecord{Kind: "released", GrantSequence: intents[i]}
	}
	if _, err := batched.appendBatch(releases); err != nil {
		t.Fatal(err)
	}
	if len(single.state.pending) != 0 || len(batched.state.pending) != 0 || len(single.state.granted) != count || len(batched.state.granted) != count {
		t.Fatal("state differs", len(single.state.pending), len(batched.state.pending))
	}
	if single.bytes != batched.bytes || len(single.records) != len(batched.records) {
		t.Fatal("journal size differs", single.bytes, batched.bytes)
	}
	a, err := os.ReadFile(singlePath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(batchedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("batched journal bytes differ from separate appends")
	}
	reloaded, err := os.OpenFile(batchedPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	replayed, err := loadGrantJournal(reloaded)
	if err != nil || len(replayed.state.pending) != 0 || len(replayed.state.granted) != count {
		t.Fatal("replay", err)
	}
}

func TestGrantJournalBatchRejectionLeavesStateUntouched(t *testing.T) {
	j, objects := costJournal(t, 4)
	before, records := j.bytes, len(j.records)
	stranger := journalObject{Volume: 17, ID: [16]byte{0xee, 1}}
	for name, batch := range map[string][]journalRecord{
		"object outside the workspace snapshot": append(grantBatch(objects[:2]), costGrant(stranger)),
		"repeated object in one batch":          append(grantBatch(objects[:2]), costGrant(objects[1])),
		"foreign record kind":                   append(grantBatch(objects[:2]), journalRecord{Kind: "complete"}),
	} {
		if _, err := j.appendBatch(batch); err == nil {
			t.Fatalf("%s accepted", name)
		}
		if j.bytes != before || len(j.records) != records || len(j.state.granted) != 0 || len(j.state.pending) != 0 || j.blocked != nil {
			t.Fatalf("%s changed the journal: bytes=%d records=%d granted=%d pending=%d blocked=%v", name, j.bytes, len(j.records), len(j.state.granted), len(j.state.pending), j.blocked)
		}
	}
	sequences, err := j.appendBatch(grantBatch(objects))
	if err != nil || sequences[0] != uint64(records+1) {
		t.Fatal("append after rejection", sequences, err)
	}
	// A release naming an unknown intent rejects the whole release batch.
	releases := []journalRecord{{Kind: "released", GrantSequence: sequences[0]}, {Kind: "released", GrantSequence: sequences[0] + 1000}}
	if _, err := j.appendBatch(releases); err == nil {
		t.Fatal("release of an unrecorded intent accepted")
	}
	if len(j.state.pending) != len(objects) {
		t.Fatal("rejected release batch consumed an intent", len(j.state.pending))
	}
	releases = []journalRecord{{Kind: "released", GrantSequence: sequences[0]}, {Kind: "released", GrantSequence: sequences[0]}}
	if _, err := j.appendBatch(releases); err == nil || len(j.state.pending) != len(objects) {
		t.Fatal("duplicate release in one batch accepted", err)
	}
}

func TestGrantJournalBatchFailedFlushPoisonsAndRestoresState(t *testing.T) {
	j, objects := costJournal(t, 4)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.appendBatch(grantBatch(objects)); err == nil || j.blocked == nil {
		t.Fatal("failed write did not poison the journal", err)
	}
	if len(j.state.granted) != 0 || len(j.state.pending) != 0 {
		t.Fatal("unflushed intents advanced recovery ownership")
	}
	if _, err := j.appendBatch(grantBatch(objects)); err == nil {
		t.Fatal("poisoned journal accepted another batch")
	}
}
