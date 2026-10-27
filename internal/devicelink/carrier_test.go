package devicelink

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSplitDataRoundTrip(t *testing.T) {
	for _, size := range []int{1, chunkBytes, chunkBytes + 1, 5*chunkBytes + 17, MaxFrameBytes} {
		payload := bytes.Repeat([]byte{'x'}, size)
		frames := splitData("s", Packet{Sequence: 7, Payload: payload})
		if (size <= chunkBytes) != (len(frames) == 1) {
			t.Fatalf("size %d produced %d frames", size, len(frames))
		}
		var join assembler
		var got []byte
		for i, f := range frames {
			out, done, err := join.add(f)
			if err != nil || done != (i == len(frames)-1) {
				t.Fatalf("size %d frame %d: done=%v err=%v", size, i, done, err)
			}
			got = out
		}
		if !bytes.Equal(got, payload) || join.pending() {
			t.Fatalf("size %d reassembled wrongly", size)
		}
	}
}

func TestAssemblerRejectsBrokenSequences(t *testing.T) {
	frames := splitData("s", Packet{Sequence: 1, Payload: bytes.Repeat([]byte{'x'}, 3*chunkBytes)})
	cases := map[string][]frame{
		"skipped part":      {frames[0], frames[2]},
		"starts mid-packet": {frames[1]},
		"unsplit inside":    {frames[0], {Type: "data", SessionID: "s", Sequence: 2, Payload: []byte("{}")}},
		"other session":     {frames[0], {Type: "data", SessionID: "t", Sequence: 1, Part: 1, Parts: 3, Payload: []byte("x")}},
		"too many parts":    {{Type: "data", SessionID: "s", Sequence: 1, Part: 0, Parts: maxParts + 1, Payload: []byte("x")}},
	}
	for name, seq := range cases {
		var join assembler
		var err error
		for _, f := range seq {
			if _, _, err = join.add(f); err != nil {
				break
			}
		}
		if err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestManagerCarriesLargeMessagesWithoutPingTimeout(t *testing.T) {
	// Nine megabytes through the race detector takes about ten seconds; a loaded runner needs more.
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pairTestNodes(t, a, b)
	session := openClient(t, ctx, a, b)
	defer session.Close()
	big := strings.Repeat("y", 3<<20)
	for range 3 {
		if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "count.once", Arguments: map[string]any{"pad": big}}); err != nil {
			t.Fatal(err)
		}
	}
	epoch := a.manager.Activity(b.id.ID).Epoch
	time.Sleep(1500 * time.Millisecond)
	if a.manager.Activity(b.id.ID).Epoch != epoch {
		t.Fatal("idle link was replaced")
	}
}
