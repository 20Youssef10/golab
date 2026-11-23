package runtime

import (
	"encoding/json"
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
