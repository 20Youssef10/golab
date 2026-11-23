package runtime

import "encoding/json"

// Accumulator reproduces jupyter_kernel_client's output_hook exactly
// (docs/02 §3.5): adjacent streams are NOT merged, update_display_data
// replaces matching outputs in place, and clear_output ignores `wait`.
type Accumulator struct {
	outputs  []Output
	onOutput func(Output)
}

func NewAccumulator(onOutput func(Output)) *Accumulator {
	return &Accumulator{onOutput: onOutput}
}

// Outputs returns a copy so callers cannot mutate the accumulator's state.
func (a *Accumulator) Outputs() []Output {
	return append([]Output(nil), a.outputs...)
}

func (a *Accumulator) append(o Output) {
	a.outputs = append(a.outputs, o)
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
	}
	return nil
}
