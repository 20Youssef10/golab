package runtime

import (
	"fmt"
	"strings"
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

// retainedBytes measures the MIME-bundle payloads that dominate a real
// notebook (base64 images live in Output.Data, not in Output.Text).
func retainedBytes(outs []Output) int {
	n := 0
	for _, o := range outs {
		n += len(o.Text) + len(o.EName) + len(o.EValue)
		for _, s := range o.Traceback {
			n += len(s)
		}
		for _, m := range []map[string]any{o.Data, o.Metadata} {
			for k, v := range m {
				n += len(k)
				switch x := v.(type) {
				case string:
					n += len(x)
				case []byte:
					n += len(x)
				}
			}
		}
	}
	return n
}

func TestJournalByteBudgetBoundsLargeOutputs(t *testing.T) {
	// A count cap of 256 is not a memory bound: 256 64-MiB base64 images would
	// retain multi-GB. The snapshot must also evict oldest-first on a byte
	// budget, while keeping the most recent output.
	const byteCap = 8 << 20 // must match journalBytesCap
	const perOutput = 2 << 20
	chunk := strings.Repeat("A", perOutput)

	j := newJournal("exec-bytes")
	acc := make([]Output, 0, 24)
	for i := 0; i < 24; i++ {
		acc = append(acc, Output{
			OutputType: "display_data",
			Text:       fmt.Sprintf("o-%d", i),
			Data:       map[string]any{"image/png": chunk},
		})
		j.setOutputs(acc)
		if got := retainedBytes(j.snapshot()); got > byteCap {
			t.Fatalf("iteration %d: retained %d bytes, budget %d", i, got, byteCap)
		}
	}
	if len(j.outs) == 0 {
		t.Fatal("byte eviction dropped every output")
	}
	if len(j.outs) >= journalOutsCap {
		t.Fatalf("byte budget was ignored: kept %d outputs", len(j.outs))
	}
	if got := j.outs[len(j.outs)-1].Text; got != "o-23" {
		t.Fatalf("eviction did not keep the newest output, got %q", got)
	}
}
