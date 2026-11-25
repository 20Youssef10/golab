package runtime

import "encoding/json"

// maxAccumulatedOutputs bounds the live output window. It is a multiple of
// journalOutsCap so the journal's resume snapshot still sees the same trailing
// outputs it did before accumulation was bounded.
const maxAccumulatedOutputs = 1024

// Accumulator reproduces jupyter_kernel_client's output_hook exactly
// (docs/02 §3.5): adjacent streams are NOT merged, update_display_data
// replaces matching outputs in place, and clear_output ignores `wait`.
//
// outputs is a fixed-capacity ring buffer: once full, each new output
// overwrites the oldest slot in O(1), so a long stream neither retains an
// unbounded history nor copies the whole history on every append.
type Accumulator struct {
	outputs  []Output // logical order is outputs[head:] followed by outputs[:head]
	head     int      // index of the oldest retained output; 0 until full
	onOutput func(Output)
}

func NewAccumulator(onOutput func(Output)) *Accumulator {
	return &Accumulator{onOutput: onOutput}
}

// Outputs returns a copy of at most maxAccumulatedOutputs recent outputs in
// insertion order, so callers cannot mutate the accumulator's state and never
// pay for a copy of the full history.
func (a *Accumulator) Outputs() []Output {
	if len(a.outputs) == 0 {
		return nil
	}
	out := make([]Output, 0, len(a.outputs))
	if a.head == 0 {
		return append(out, a.outputs...)
	}
	out = append(out, a.outputs[a.head:]...)
	return append(out, a.outputs[:a.head]...)
}

func (a *Accumulator) append(o Output) {
	if len(a.outputs) < maxAccumulatedOutputs {
		a.outputs = append(a.outputs, o)
	} else {
		a.outputs[a.head] = o
		a.head = (a.head + 1) % maxAccumulatedOutputs
	}
	if a.onOutput != nil {
		a.onOutput(o)
	}
}

// Feed applies one iopub message. Unknown types are ignored. An error is
// returned only when a known type carries malformed JSON.
func (a *Accumulator) Feed(m *Message) error {
	switch m.Header.MsgType {
	case "execute_result":
		var o Output
		if err := json.Unmarshal(m.Content, &o); err != nil {
			return err
		}
		o.OutputType = "execute_result"
		a.append(o)
	case "stream":
		var o Output
		if err := json.Unmarshal(m.Content, &o); err != nil {
			return err
		}
		o.OutputType = "stream"
		a.append(o)
	case "display_data":
		var o Output
		if err := json.Unmarshal(m.Content, &o); err != nil {
			return err
		}
		o.OutputType = "display_data"
		a.append(o)
	case "error":
		var o Output
		if err := json.Unmarshal(m.Content, &o); err != nil {
			return err
		}
		o.OutputType = "error"
		a.append(o)
	case "clear_output":
		// `wait` is deliberately ignored: upstream clears immediately.
		a.outputs = nil
		a.head = 0
		// The streaming view is built entirely from onOutput callbacks, so a
		// silent clear would diverge from the final Result. Emit a synthetic
		// control event (it is NOT appended, so Result and retention are
		// unchanged) to tell consumers to clear their view too (gap-report §4.3).
		if a.onOutput != nil {
			a.onOutput(Output{OutputType: "clear_output"})
		}
	case "update_display_data":
		var update struct {
			Data      map[string]any `json:"data"`
			Metadata  map[string]any `json:"metadata"`
			Transient struct {
				DisplayID string `json:"display_id"`
			} `json:"transient"`
		}
		if err := json.Unmarshal(m.Content, &update); err != nil {
			return err
		}
		id := update.Transient.DisplayID
		for i := range a.outputs {
			if a.outputs[i].Transient != nil && a.outputs[i].Transient.DisplayID == id {
				a.outputs[i].Data = update.Data
				a.outputs[i].Metadata = update.Metadata
			}
		}
		// Emit a synthetic delta carrying the new MIME bundle and display id so
		// streaming consumers can apply the same in-place replacement. As with
		// clear_output this event is not appended to the accumulator, leaving
		// the bounded ring buffer and the final Result untouched.
		if a.onOutput != nil {
			a.onOutput(Output{
				OutputType: "update_display_data",
				Data:       update.Data,
				Metadata:   update.Metadata,
				Transient:  &Transient{DisplayID: id},
			})
		}
	}
	return nil
}
