package runtime

import (
	"fmt"
	"testing"
)

// TestJournalBounds pins the honest thin-layer caps of the recovery journal:
// a bounded dedup ring, a bounded output snapshot and a bounded number of live
// journals per kernel, with terminal eviction. These caps keep a long-running
// execution from growing memory without bound.
func TestJournalBounds(t *testing.T) {
	j := newJournal("exec-1")
	msgWithID := func(id string) *Message {
		m := newMessage("stream", "iopub", "s", MessageHeader{},
			map[string]any{"name": "stdout", "text": "x"})
		m.Header.MsgID = id
		return m
	}

	// More than the dedup ring: every one must be observed as new, but the
	// ring itself must never exceed its cap.
	for i := 0; i < journalSeenCap+50; i++ {
		if !j.observe(msgWithID(fmt.Sprintf("m-%d", i))) {
			t.Fatalf("message %d was wrongly deduped", i)
		}
	}
	if len(j.seen) > journalSeenCap {
		t.Fatalf("dedup ring grew to %d (cap %d)", len(j.seen), journalSeenCap)
	}
	// The oldest ids must have been evicted (observed as new again); the newest
	// must still be in the ring (observed as a duplicate).
	if !j.observe(msgWithID("m-0")) {
		t.Fatal("evicted oldest id should be observed as new")
	}
	if j.observe(msgWithID(fmt.Sprintf("m-%d", journalSeenCap+49))) {
		t.Fatal("newest id should still be deduped")
	}

	// Output snapshot is capped as well, keeping the most recent outputs.
	outs := make([]Output, 0, journalOutsCap+40)
	for i := 0; i < journalOutsCap+40; i++ {
		outs = append(outs, Output{OutputType: "stream", Text: fmt.Sprintf("o-%d", i)})
	}
	j.setOutputs(outs)
	if len(j.outs) > journalOutsCap {
		t.Fatalf("output snapshot grew to %d (cap %d)", len(j.outs), journalOutsCap)
	}
	if j.outs[len(j.outs)-1].Text != fmt.Sprintf("o-%d", journalOutsCap+39) {
		t.Fatalf("output snapshot did not keep the newest output: %+v", j.outs[len(j.outs)-1])
	}

	// A kernel never keeps more than maxJournals journals; the oldest goes.
	k := &kernel{}
	for i := 0; i < maxJournals+3; i++ {
		k.addJournal(newJournal(fmt.Sprintf("exec-%d", i)))
	}
	if len(k.journals) > maxJournals {
		t.Fatalf("kept %d journals (cap %d)", len(k.journals), maxJournals)
	}
	if k.lookupJournal("exec-0") != nil {
		t.Fatal("oldest journal was not evicted")
	}
	if k.lookupJournal(fmt.Sprintf("exec-%d", maxJournals+2)) == nil {
		t.Fatal("newest journal was evicted")
	}
	// Terminal eviction removes exactly one journal.
	k.deleteJournal(fmt.Sprintf("exec-%d", maxJournals+2))
	if k.lookupJournal(fmt.Sprintf("exec-%d", maxJournals+2)) != nil {
		t.Fatal("terminal journal was not deleted")
	}
}
