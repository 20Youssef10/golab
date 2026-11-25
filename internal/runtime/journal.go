package runtime

// The journal is an HONEST thin recovery layer, not a replay server. iopub has
// no acknowledgement/resend protocol and the REST API has no output endpoint,
// so messages emitted strictly while the channels socket is down cannot be
// replayed by this client. The journal only remembers what this process has
// actually observed, with hard caps so a long execution cannot grow memory
// without bound.
const (
	journalSeenCap  = 256     // inbound msg ids kept for dedup
	journalOutsCap  = 256     // output snapshot copies kept for resume
	journalBytesCap = 8 << 20 // 8 MiB of output payload kept for resume
	maxJournals     = 4       // live journals retained per kernel
)

// journal tracks one execution's observed messages and outputs. A message id
// is recorded by observe BEFORE the accumulator feeds on it: clear_output and
// update_display_data are not idempotent, so a replayed duplicate must never
// reach the accumulator a second time.
type journal struct {
	msgID     string
	seen      []string
	outs      []Output
	outBytes  int  // payload bytes retained in outs (base64 images dominate)
	delivered int  // outputs already emitted to the caller
	dropped   bool // the socket dropped at least once during this execution
	sawIdle   bool // terminal idle was observed; survives Resume
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

// setOutputs refreshes the bounded snapshot from the live accumulator. The
// snapshot is capped BOTH by count and by bytes: a count cap alone lets a few
// large base64 images retain gigabytes. Eviction is oldest-first, keeping the
// newest outputs. If the snapshot shrinks (clear_output or eviction), delivered
// is clamped so it stays a valid count of the outputs held here.
func (j *journal) setOutputs(outs []Output) {
	prev := len(j.outs)
	j.outs, j.outBytes = boundedTail(outs, journalOutsCap, journalBytesCap)
	if shrink := prev - len(j.outs); shrink > 0 {
		if j.delivered > shrink {
			j.delivered -= shrink
		} else {
			j.delivered = 0
		}
	}
}

// boundedTail copies the most recent outputs that fit both caps. A single
// output larger than the byte budget is dropped wholesale so the retained
// snapshot stays strictly bounded.
func boundedTail(src []Output, countCap, byteCap int) ([]Output, int) {
	if countCap <= 0 || byteCap <= 0 || len(src) == 0 {
		return nil, 0
	}
	// The count cap delimits the trailing window (the most recent outputs),
	// not a prefix of src.
	oldest := 0
	if len(src) > countCap {
		oldest = len(src) - countCap
	}
	total := 0
	keep := len(src) // exclusive start of the retained suffix
	for i := len(src) - 1; i >= oldest; i-- {
		n := outputBytes(src[i])
		if total+n > byteCap {
			break
		}
		total += n
		keep = i
	}
	if keep == len(src) {
		return nil, 0
	}
	return append([]Output(nil), src[keep:]...), total
}

// outputBytes approximates an output's retained payload. Strings (the base64
// image bodies in Data) dominate and are counted exactly; scalar overhead is
// ignored because it cannot grow with message size.
func outputBytes(o Output) int {
	n := len(o.OutputType) + len(o.Name) + len(o.Text) + len(o.EName) + len(o.EValue)
	if o.ExecutionCount != nil {
		n += 8
	}
	if o.Transient != nil {
		n += len(o.Transient.DisplayID)
	}
	for _, s := range o.Traceback {
		n += len(s)
	}
	n += valueBytes(o.Data)
	n += valueBytes(o.Metadata)
	return n
}

func valueBytes(v any) int {
	switch x := v.(type) {
	case string:
		return len(x)
	case []byte:
		return len(x)
	case []any:
		n := 0
		for _, e := range x {
			n += valueBytes(e)
		}
		return n
	case map[string]any:
		n := 0
		for k, e := range x {
			n += len(k) + valueBytes(e)
		}
		return n
	default:
		return 0
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
