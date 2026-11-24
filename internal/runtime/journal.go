package runtime

// The journal is an HONEST thin recovery layer, not a replay server. iopub has
// no acknowledgement/resend protocol and the REST API has no output endpoint,
// so messages emitted strictly while the channels socket is down cannot be
// replayed by this client. The journal only remembers what this process has
// actually observed, with hard caps so a long execution cannot grow memory
// without bound.
const (
	journalSeenCap = 256 // inbound msg ids kept for dedup
	journalOutsCap = 256 // output snapshot copies kept for resume
	maxJournals    = 4   // live journals retained per kernel
)

// journal tracks one execution's observed messages and outputs. A message id
// is recorded by observe BEFORE the accumulator feeds on it: clear_output and
// update_display_data are not idempotent, so a replayed duplicate must never
// reach the accumulator a second time.
type journal struct {
	msgID     string
	seen      []string
	outs      []Output
	delivered int  // outputs already emitted to the caller
	dropped   bool // the socket dropped at least once during this execution
	terminal  bool // idle + execute_reply were both observed
}

func newJournal(msgID string) *journal { return &journal{msgID: msgID} }

// observe records msg's id and reports whether it is new. An empty msg id is
// always treated as new (it cannot be deduplicated reliably).
func (j *journal) observe(msg *Message) bool {
	id := msg.Header.MsgID
	if id == "" {
		return true
	}
	for _, s := range j.seen {
		if s == id {
			return false
		}
	}
	j.seen = appendRing(j.seen, id, journalSeenCap)
	return true
}

// setOutputs refreshes the bounded snapshot from the live accumulator. If the
// snapshot shrinks (clear_output), delivered is clamped so it stays a valid
// count of the outputs held here.
func (j *journal) setOutputs(outs []Output) {
	prev := len(j.outs)
	j.outs = lastN(outs, journalOutsCap)
	if shrink := prev - len(j.outs); shrink > 0 {
		if j.delivered > shrink {
			j.delivered -= shrink
		} else {
			j.delivered = 0
		}
	}
}

// snapshot returns a copy so callers cannot mutate the journal's state.
func (j *journal) snapshot() []Output {
	return append([]Output(nil), j.outs...)
}

// seed returns the outputs already delivered; a resumed accumulator starts
// from these so they are never emitted a second time.
func (j *journal) seed() []Output { return j.snapshot() }

func appendRing[T any](ring []T, v T, cap int) []T {
	if len(ring) < cap {
		return append(ring, v)
	}
	copy(ring, ring[1:])
	ring[cap-1] = v
	return ring
}

// lastN copies at most n trailing elements of src (the most recent outputs).
func lastN[T any](src []T, n int) []T {
	if n <= 0 {
		return nil
	}
	if len(src) <= n {
		return append([]T(nil), src...)
	}
	return append([]T(nil), src[len(src)-n:]...)
}

// --- journal store on kernel -------------------------------------------------

// Journals are keyed by execution msg id and bounded to maxJournals. The
// insertion order gives deterministic oldest-first eviction. All access is
// mutex-guarded because Execute/Resume may be driven from different goroutines.
func (k *kernel) addJournal(j *journal) {
	k.journalMu.Lock()
	defer k.journalMu.Unlock()
	if k.journals == nil {
		k.journals = map[string]*journal{}
	}
	if _, exists := k.journals[j.msgID]; exists {
		return
	}
	k.journals[j.msgID] = j
	k.journalOrder = append(k.journalOrder, j.msgID)
	for len(k.journalOrder) > maxJournals {
		oldest := k.journalOrder[0]
		k.journalOrder = k.journalOrder[1:]
		delete(k.journals, oldest)
	}
}

func (k *kernel) lookupJournal(msgID string) *journal {
	k.journalMu.Lock()
	defer k.journalMu.Unlock()
	return k.journals[msgID]
}

func (k *kernel) deleteJournal(msgID string) {
	k.journalMu.Lock()
	defer k.journalMu.Unlock()
	delete(k.journals, msgID)
	for i, id := range k.journalOrder {
		if id == msgID {
			k.journalOrder = append(k.journalOrder[:i], k.journalOrder[i+1:]...)
			break
		}
	}
}
