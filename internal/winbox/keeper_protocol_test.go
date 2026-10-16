package winbox

import (
	"strings"
	"testing"
)

func keeperTestObject(volume uint64, id byte, directory bool) journalObject {
	o := journalObject{Volume: volume, Directory: directory}
	o.ID[0] = id
	return o
}

func TestKeeperProtocol(t *testing.T) {
	root, anchor, state := keeperTestObject(7, 1, true), keeperTestObject(7, 2, true), keeperTestObject(7, 3, true)
	valid := func() keeperInit {
		return keeperInit{Key: root, Journal: `C:\state\x.journal`, Holds: []keeperHold{
			{Handle: 4, Object: anchor, Role: keeperRoleAnchor},
			{Handle: 8, Object: root, Role: keeperRoleRoot},
			{Handle: 12, Object: state, Role: keeperRoleJourn},
		}}
	}
	if err := (keeperRequest{Sequence: 1, Operation: "init", Init: ptr(valid())}).validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*keeperInit){
		"no holds":        func(i *keeperInit) { i.Holds = nil },
		"no journal":      func(i *keeperInit) { i.Journal = "" },
		"repeated handle": func(i *keeperInit) { i.Holds[2].Handle = 4 },
		"repeated object": func(i *keeperInit) { i.Holds[2].Object = anchor },
		"file hold":       func(i *keeperInit) { i.Holds[2].Object.Directory = false },
		"zero identity":   func(i *keeperInit) { i.Holds[2].Object = journalObject{Volume: 7, Directory: true} },
		"no anchor":       func(i *keeperInit) { i.Holds[0].Role = keeperRoleParent },
		"no root":         func(i *keeperInit) { i.Holds[1].Role = keeperRoleParent },
		"two roots":       func(i *keeperInit) { i.Holds[2].Role = keeperRoleRoot },
		"key mismatch":    func(i *keeperInit) { i.Key = state },
		"file key":        func(i *keeperInit) { i.Key.Directory = false },
		"unknown role":    func(i *keeperInit) { i.Holds[2].Role = "other" },
		"import at init":  func(i *keeperInit) { i.Holds[2].Role = keeperRoleImport },
		"huge path":       func(i *keeperInit) { i.Journal = strings.Repeat("a", maxKeeperPath+1) },
	} {
		t.Run(name, func(t *testing.T) {
			init := valid()
			init.Holds = append([]keeperHold(nil), init.Holds...)
			mutate(&init)
			if err := (keeperRequest{Sequence: 1, Operation: "init", Init: &init}).validate(); err == nil {
				t.Fatal("invalid initialization accepted")
			}
		})
	}
	file := keeperTestObject(7, 9, false)
	for name, request := range map[string]keeperRequest{
		"zero sequence":      {Operation: "status"},
		"init later":         {Sequence: 2, Operation: "init", Init: ptr(valid())},
		"init payload":       {Sequence: 2, Operation: "status", Init: ptr(valid())},
		"hold without body":  {Sequence: 2, Operation: "hold"},
		"hold wrong role":    {Sequence: 2, Operation: "hold", Hold: &keeperHold{Handle: 4, Object: anchor, Role: keeperRoleRoot}},
		"hold zero handle":   {Sequence: 2, Operation: "hold", Hold: &keeperHold{Object: anchor, Role: keeperRoleImport}},
		"journal directory":  {Sequence: 2, Operation: "journal", Journal: &anchor},
		"journal no payload": {Sequence: 2, Operation: "journal"},
		"status with hold":   {Sequence: 2, Operation: "status", Hold: &keeperHold{Handle: 4, Object: anchor, Role: keeperRoleImport}},
		"unknown":            {Sequence: 2, Operation: "adopt"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.validate(); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	for _, request := range []keeperRequest{
		{Sequence: 2, Operation: "status"}, {Sequence: 3, Operation: "release"},
		{Sequence: 2, Operation: "journal", Journal: &file},
		{Sequence: 2, Operation: "hold", Hold: &keeperHold{Handle: 4, Object: anchor, Role: keeperRoleImport}},
	} {
		if err := request.validate(); err != nil {
			t.Fatal(request.Operation, err)
		}
	}
	if keeperEventName(root) == keeperEventName(state) || keeperPipeName(root) == keeperPipeName(state) || !strings.HasPrefix(keeperEventName(root), `Global\`) {
		t.Fatal("lease names do not separate keys")
	}
}

func ptr[T any](v T) *T { return &v }

func TestKeeperReleaseDecision(t *testing.T) {
	bound, other := keeperTestObject(7, 5, false), keeperTestObject(7, 6, false)
	complete := &grantJournal{records: []journalRecord{{}}}
	complete.state.complete = true
	pending := &grantJournal{records: []journalRecord{{}}}
	executing := &grantJournal{records: []journalRecord{{}}}
	executing.state.complete = true
	executing.state.executions = map[uint64]bool{1: true}
	damaged := &grantJournal{records: []journalRecord{{}}, blocked: errJournalCorrupt}
	empty := &grantJournal{}
	for name, c := range map[string]struct {
		bound  *journalObject
		exists bool
		actual journalObject
		j      *grantJournal
		ok     bool
	}{
		"never created":        {nil, false, journalObject{}, nil, true},
		"vanished":             {&bound, false, journalObject{}, nil, false},
		"unbound empty":        {nil, true, other, empty, true},
		"unbound with records": {nil, true, other, pending, false},
		"unbound damaged":      {nil, true, other, &grantJournal{blocked: errJournalCorrupt}, false},
		"unreadable":           {&bound, true, bound, nil, false},
		"bound complete":       {&bound, true, bound, complete, true},
		"substituted file":     {&bound, true, other, complete, false},
		"bound incomplete":     {&bound, true, bound, pending, false},
		"bound executions":     {&bound, true, bound, executing, false},
		"bound damaged":        {&bound, true, bound, damaged, false},
		"bound empty":          {&bound, true, bound, empty, false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := keeperReleaseError(c.bound, c.exists, c.actual, c.j); (err == nil) != c.ok {
				t.Fatal("unexpected decision", err)
			}
		})
	}
}
