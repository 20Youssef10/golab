package runtime

import (
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"testing"
)

func msg(msgType string, content string) *Message {
	return &Message{
		Header:  MessageHeader{MsgType: msgType},
		Content: json.RawMessage(content),
	}
}

func TestAccumulatorExactSemantics(t *testing.T) {
	// Ports jupyter_kernel_client.client.output_hook semantics from docs/02 §3.5
	// (and the output assertions in upstream tests/test_streaming.py).
	var streamed []Output
	acc := NewAccumulator(func(o Output) { streamed = append(streamed, o) })

	acc.Feed(msg("stream", `{"name":"stdout","text":"a"}`))
	acc.Feed(msg("stream", `{"name":"stdout","text":"b"}`))
	got := acc.Outputs()
	if len(got) != 2 {
		t.Fatalf("adjacent streams must NOT be merged, got %d outputs: %+v", len(got), got)
	}
	if got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("stream text wrong: %+v", got)
	}

	acc.Feed(DisplayDataMsg("img", "one"))
	if got := acc.Outputs(); len(got) != 3 || got[2].Transient == nil || got[2].Transient.DisplayID != "img" {
		t.Fatalf("display_data not appended correctly: %+v", acc.Outputs())
	}

	acc.Feed(msg("update_display_data",
		`{"data":{"text/plain":"two"},"metadata":{},"transient":{"display_id":"img"}}`))
	got = acc.Outputs()
	if len(got) != 3 || got[2].Data["text/plain"] != "two" || got[0].Text != "a" {
		t.Fatalf("update_display_data must replace in place: %+v", got)
	}

	// clear_output ignores `wait` and removes everything.
	acc.Feed(msg("clear_output", `{"wait":true}`))
	if len(acc.Outputs()) != 0 {
		t.Fatalf("clear_output must empty the accumulator: %+v", acc.Outputs())
	}

	acc.Feed(msg("error", `{"ename":"ValueError","evalue":"bad","traceback":["tb"]}`))
	acc.Feed(msg("execute_result", `{"data":{"text/plain":"7"},"metadata":{},"execution_count":7}`))
	outs := acc.Outputs()
	if len(outs) != 2 || outs[0].OutputType != "error" || outs[0].EName != "ValueError" ||
		outs[1].OutputType != "execute_result" || outs[1].ExecutionCount == nil || *outs[1].ExecutionCount != 7 {
		t.Fatalf("error/execute_result accumulation wrong: %+v", outs)
	}

	// The streaming hook fires once per newly appended output, not on updates
	// or clears. We appended: stream, stream, display, error, execute_result.
	if len(streamed) != 5 {
		t.Fatalf("streaming hook fired %d times, want 5", len(streamed))
	}
}

func DisplayDataMsg(id, text string) *Message {
	return msg("display_data", `{"data":{"text/plain":"`+text+`"},"metadata":{},"transient":{"display_id":"`+id+`"}}`)
}

func TestAccumulatorIgnoresUnknownTypes(t *testing.T) {
	acc := NewAccumulator(nil)
	for _, m := range []*Message{
		msg("status", `{"execution_state":"idle"}`),
		msg("execute_input", `{"code":"x"}`),
		msg("colab_request", `{}`),
	} {
		if err := acc.Feed(m); err != nil {
			t.Fatalf("Feed(%s): %v", m.Header.MsgType, err)
		}
	}
	if len(acc.Outputs()) != 0 {
		t.Fatalf("unknown message types must be ignored: %+v", acc.Outputs())
	}
}

// Regression for the confirmed §3.2 defect: Outputs() deep-copied the entire
// unbounded slice on every iopub message (kernel.go:259), so a long stream
// performed ~N full-slice copies and retained everything forever.
const wantOutputCap = 1024

func feedStreams(t *testing.T, acc *Accumulator, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		if err := acc.Feed(msg("stream", fmt.Sprintf(`{"name":"stdout","text":"o-%d"}`, i))); err != nil {
			t.Fatalf("Feed(stream o-%d): %v", i, err)
		}
	}
}

func TestAccumulatorBoundedRetention(t *testing.T) {
	// Ports the bounded-retention expectation for the iopub output hook:
	// retention must stop at a fixed cap, with the newest outputs kept in order.
	acc := NewAccumulator(nil)
	const extra = 512
	feedStreams(t, acc, 0, wantOutputCap+extra)

	got := acc.Outputs()
	if len(got) != wantOutputCap {
		t.Fatalf("retained %d outputs, want cap %d", len(got), wantOutputCap)
	}
	if cap(got) > wantOutputCap {
		t.Fatalf("Outputs() backing array has cap %d, want <= %d (full-history copy)", cap(got), wantOutputCap)
	}
	for i, o := range got {
		if want := fmt.Sprintf("o-%d", extra+i); o.Text != want {
			t.Fatalf("output %d = %q, want %q (newest outputs must be kept in order)", i, o.Text, want)
		}
	}
	// Preserve the defensive-copy contract: mutating the returned slice must
	// not change what the accumulator holds.
	got[0].Text = "mutated"
	if again := acc.Outputs(); again[0].Text != fmt.Sprintf("o-%d", extra) {
		t.Fatalf("Outputs() aliases internal state: got %q", again[0].Text)
	}
}

var outputSink []Output

// bytesPerOutputsCall reports the mean bytes allocated by one Outputs() call.
func bytesPerOutputsCall(acc *Accumulator, calls int) uint64 {
	goruntime.GC()
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	for i := 0; i < calls; i++ {
		outputSink = acc.Outputs()
	}
	goruntime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(calls)
}

func TestAccumulatorRingWraparoundSemantics(t *testing.T) {
	// Wrapping must not disturb insertion order, in-place display updates or
	// clear_output's reset of the ring.
	acc := NewAccumulator(nil)
	feedStreams(t, acc, 0, wantOutputCap)
	if err := acc.Feed(DisplayDataMsg("img", "one")); err != nil {
		t.Fatal(err)
	}
	feedStreams(t, acc, wantOutputCap, wantOutputCap+19)

	outs := acc.Outputs()
	if len(outs) != wantOutputCap || outs[0].Text != "o-20" ||
		outs[wantOutputCap-1].Text != fmt.Sprintf("o-%d", wantOutputCap+18) {
		t.Fatalf("wrapped order wrong: len=%d first=%q last=%q", len(outs), outs[0].Text, outs[len(outs)-1].Text)
	}
	img := wantOutputCap - 20 // the display output sits just before the 19 newer streams
	if outs[img].Transient == nil || outs[img].Transient.DisplayID != "img" {
		t.Fatalf("display output lost on wrap: %+v", outs[img])
	}
	if err := acc.Feed(msg("update_display_data",
		`{"data":{"text/plain":"two"},"metadata":{},"transient":{"display_id":"img"}}`)); err != nil {
		t.Fatal(err)
	}
	outs = acc.Outputs()
	if outs[img].Data["text/plain"] != "two" || outs[img+1].Text != fmt.Sprintf("o-%d", wantOutputCap) {
		t.Fatalf("update after wrap must replace in place: %+v", outs[img:img+2])
	}

	if err := acc.Feed(msg("clear_output", `{}`)); err != nil {
		t.Fatal(err)
	}
	feedStreams(t, acc, 0, 3)
	outs = acc.Outputs()
	if len(outs) != 3 || outs[0].Text != "o-0" || outs[2].Text != "o-2" {
		t.Fatalf("clear after wrap must reset ring order: %+v", outs)
	}
}

func TestAccumulatorOutputsCopyIsBounded(t *testing.T) {
	// kernel.go calls Outputs() after every message. A full-history copy makes
	// per-call allocations grow linearly with the number of messages processed.
	acc := NewAccumulator(nil)
	feedStreams(t, acc, 0, wantOutputCap+1)
	perCallAtCap := bytesPerOutputsCall(acc, 128)

	// Double the history. A bounded copy allocates exactly the same window;
	// an unbounded full slice copy doubles its per-call allocation.
	feedStreams(t, acc, wantOutputCap+1, 2*wantOutputCap+1)
	perCallAtDouble := bytesPerOutputsCall(acc, 128)

	if perCallAtDouble > perCallAtCap+perCallAtCap/4 {
		t.Fatalf("Outputs() copy grew with history: %d bytes/call at %d outputs vs %d bytes/call at %d outputs",
			perCallAtCap, wantOutputCap+1, perCallAtDouble, 2*wantOutputCap+1)
	}
}
