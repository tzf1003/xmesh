package winbox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func journalFixture(t *testing.T) (*grantJournal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recovery.journal")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	j, err := loadGrantJournal(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, path
}

func journalProfile() journalRecord {
	return journalRecord{Kind: "profile", Profile: "xmesh-l2-0123456789abcdef0123456789abcdef", SID: "S-1-15-2-1-2-3-4-5-6-7"}
}

func journalTestGrant() journalRecord {
	ace := make([]byte, 16)
	binary.LittleEndian.PutUint16(ace[2:4], 16)
	return journalRecord{Kind: "grant", Grant: &journalGrant{Object: journalObject{Volume: 17, ID: [16]byte{9}}, ACE: ace}}
}

func TestGrantJournalDurableReplayAndTransitions(t *testing.T) {
	j, path := journalFixture(t)
	if _, err := j.append(journalTestGrant()); err == nil {
		t.Fatal("grant accepted before profile ownership")
	}
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	grant := journalTestGrant()
	sequence, err := j.append(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Grant.ACE[4] = 0xff
	if j.records[1].Grant.ACE[4] == 0xff {
		t.Fatal("caller changed committed journal memory")
	}
	if _, err := j.append(journalRecord{Kind: "complete"}); err == nil {
		t.Fatal("completion accepted while a grant remained pending")
	}
	if _, err := j.append(journalRecord{Kind: "released", GrantSequence: sequence + 1}); err == nil {
		t.Fatal("release accepted for an unrecorded intent")
	}
	if _, err := j.append(journalRecord{Kind: "released", GrantSequence: sequence}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "complete"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalProfile()); err == nil {
		t.Fatal("completed journal reused")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	recovered, err := loadGrantJournal(f)
	if err != nil || !recovered.state.complete || len(recovered.records) != 4 || len(recovered.state.pending) != 0 {
		t.Fatal("durable journal state was not restored", err)
	}
}

func TestGrantJournalTornTailAndCorruption(t *testing.T) {
	j, path := journalFixture(t)
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalTestGrant()); err != nil {
		t.Fatal(err)
	}
	validPrefix := j.bytes
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	firstSize := int(binary.LittleEndian.Uint32(body[:4])) + 8
	for _, cut := range []int{1, firstSize - 1, firstSize + 1, len(body) - 1} {
		t.Run("truncation-"+strconv.Itoa(cut), func(t *testing.T) {
			broken := filepath.Join(t.TempDir(), "broken")
			if err := os.WriteFile(broken, body[:cut], 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(broken, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			prefix, err := loadGrantJournal(f)
			if !errors.Is(err, errJournalIncomplete) || prefix == nil {
				t.Fatal("torn tail was not identified", err)
			}
			expected := 0
			if cut > firstSize {
				expected = 1
			}
			if len(prefix.records) != expected {
				t.Fatal("incorrect verified prefix", len(prefix.records))
			}
			if _, err := prefix.append(journalTestGrant()); err == nil {
				t.Fatal("append accepted after torn tail")
			}
			after, err := os.ReadFile(broken)
			if err != nil || !bytes.Equal(after, body[:cut]) {
				t.Fatal("torn journal mutated", err)
			}
		})
	}
	for _, mutation := range []string{"checksum", "length", "sequence", "version", "unknown-field"} {
		t.Run(mutation, func(t *testing.T) {
			frame := bytes.Clone(body[firstSize:])
			switch mutation {
			case "checksum":
				frame[len(frame)-2] ^= 1
			case "length":
				binary.LittleEndian.PutUint32(frame[:4], maxJournalFrame+1)
			default:
				payload := bytes.Clone(frame[8:])
				if mutation == "sequence" {
					payload = bytes.Replace(payload, []byte(`"sequence":2`), []byte(`"sequence":1`), 1)
				} else if mutation == "version" {
					payload = bytes.Replace(payload, []byte(`"version":1`), []byte(`"version":2`), 1)
				} else {
					payload = append(payload[:len(payload)-1], []byte(`,"unexpected":1}`)...)
				}
				frame = make([]byte, 8+len(payload))
				binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
				binary.LittleEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(payload))
				copy(frame[8:], payload)
			}
			broken := filepath.Join(t.TempDir(), "corrupt")
			if err := os.WriteFile(broken, append(bytes.Clone(body[:firstSize]), frame...), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(broken, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			prefix, err := loadGrantJournal(f)
			if !errors.Is(err, errJournalCorrupt) || prefix == nil || len(prefix.records) != 1 {
				t.Fatal("corruption was not stopped at verified prefix", err)
			}
		})
	}
	if validPrefix != int64(len(body)) {
		t.Fatal("journal byte count mismatch")
	}
}

func TestGrantJournalFailedWritePoisonsOwner(t *testing.T) {
	j, _ := journalFixture(t)
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	// Closed descriptor exercises the uncertain write path without advancing the
	// in-memory state. Further writes stay blocked, even if a caller retries.
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalTestGrant()); err == nil {
		t.Fatal("failed journal write was accepted")
	}
	if _, err := j.append(journalTestGrant()); err == nil || len(j.records) != 1 || len(j.state.pending) != 0 {
		t.Fatal("failed journal write did not poison the owner", err)
	}
}
