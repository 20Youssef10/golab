package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const waitSlice = time.Second

// idleTracker implements the 1 s slice / reset-per-message idle limit of
// docs/02 §3.4. A zero limit disables deadline tracking entirely.
type idleTracker struct {
	limit    time.Duration
	deadline time.Time
}

func (t *idleTracker) reset(limit time.Duration) {
	t.limit = limit
	if limit <= 0 {
		t.deadline = time.Time{}
		return
	}
	t.deadline = time.Now().Add(limit)
}

func (t *idleTracker) slice() time.Duration {
	if t.deadline.IsZero() {
		return waitSlice
	}
	left := time.Until(t.deadline)
	if left <= 0 {
		return 0
	}
	if left > waitSlice {
		return waitSlice
	}
	return left
}

// shift pauses the idle deadline by d, so time spent redialing a dropped
// socket does not count as kernel silence. A zero deadline (--timeout 0,
// meaning unlimited) stays zero.
func (t *idleTracker) shift(d time.Duration) {
	if !t.deadline.IsZero() {
		t.deadline = t.deadline.Add(d)
	}
}

type kernel struct {
	id        string
	sessionID string
	rest      *restClient
	conn      *conn
	logger    *slog.Logger
	graceful  time.Duration
	closeOnce sync.Once
}

func (k *kernel) ID() string        { return k.id }
func (k *kernel) SessionID() string { return k.sessionID }

func (k *kernel) Execute(ctx context.Context, code string, opts ExecOptions) (*Result, error) {
	msgID := newMsgID()
	acc := NewAccumulator(opts.OnOutput)
	k.conn.setColabHook(opts.ColabRequestHook)
	defer k.conn.setColabHook(nil)
	k.conn.setActive(activeExec{msgID: msgID, opts: opts, session: k.sessionID})
	defer k.conn.clearActive()

	request := newMessage("execute_request", "shell", k.sessionID, MessageHeader{}, map[string]any{
		"code": code, "silent": false, "store_history": true,
		"user_expressions": map[string]any{}, "allow_stdin": opts.AllowStdin,
		"stop_on_error": true,
	})
	// Use our own id as the request id so iopub parent matching is exact.
	request.Header.MsgID = msgID
	if err := k.conn.send(ctx, request); err != nil {
		return nil, fmt.Errorf("send execute_request: %w", err)
	}

	var idle idleTracker
	idle.reset(opts.IdleTimeout)
	gotIdle := false
	interrupted := false
	ctxDone := ctx.Done()
	var reply, pendingReply *Message

	// interrupt is shared by the cancellation branch and the reconnect branch:
	// a cancellation that races with a socket drop still sends the best-effort
	// REST interrupt and enters the graceful-idle wait.
	interrupt := func() {
		// Only the first cancellation interrupts: ctx.Done() stays closed, so
		// it must be disabled or the next select would immediately take the
		// force-close branch without waiting for idle.
		ctxDone = nil
		interrupted = true
		idle.reset(k.graceful)
		if k.rest == nil {
			return
		}
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = k.rest.lifecycle(ictx, "interrupt", k.id)
		cancel()
	}

executeLoop:
	for {
		phase := "output"
		if gotIdle {
			phase = "reply"
		}
		wait := idle.slice()
		if wait <= 0 {
			if interrupted {
				// Graceful post-interrupt wait expired: force-close.
				k.conn.close()
				return nil, ctx.Err()
			}
			return nil, &TimeoutError{WaitingFor: phase, Idle: opts.IdleTimeout}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctxDone:
			timer.Stop()
			// The interrupt uses a fresh, bounded context because the caller's
			// is already cancelled.
			interrupt()
			continue

		case <-k.conn.lostChan():
			timer.Stop()
			if ctx.Err() != nil {
				// Cancellation won the race with the drop: keep the normal
				// interrupt + graceful-idle path instead of redialing.
				interrupt()
				continue
			}
			// A dropped channels socket is retried in place with the same
			// kernel/session id. The idle deadline is paused for the duration
			// so reconnection time is not mistaken for kernel silence.
			pausedAt := time.Now()
			if err := k.conn.reconnect(ctx, k.rest, k.id, k.sessionID); err != nil {
				if ctx.Err() != nil {
					interrupt()
					continue
				}
				return nil, fmt.Errorf("execute: %w", err)
			}
			idle.shift(time.Since(pausedAt))
			continue

		case <-timer.C:
			// A wait slice elapsed without a message: loop back so the
			// deadline is re-evaluated at the top (and ErrTimeout returned).
			continue

		case msg := <-k.conn.ch("iopub"):
			timer.Stop()
			accepted, err := acceptMessage(msg, msgID)
			if err != nil {
				return nil, err
			}
			if !accepted {
				// Stale messages are dropped without resetting the idle limit
				// (upstream test_output_from_other_requests_does_not_reset_the_timeout).
				continue
			}
			if !interrupted {
				idle.reset(opts.IdleTimeout)
			}
			if err := acc.Feed(msg); err != nil {
				return nil, fmt.Errorf("accumulate output: %w", err)
			}
			if msg.Header.MsgType == "status" {
				if state, _ := msg.contentMap()["execution_state"].(string); state == "idle" {
					gotIdle = true
					if pendingReply != nil {
						reply = pendingReply
						break executeLoop
					}
				}
			}

		case msg := <-k.conn.ch("shell"):
			timer.Stop()
			accepted, err := acceptMessage(msg, msgID)
			if err != nil {
				return nil, err
			}
			if !accepted || msg.Header.MsgType != "execute_reply" {
				continue
			}
			// The reply can be queued before the idle status; a single select
			// must not drop it just because it arrived a few microseconds early.
			if !gotIdle {
				pendingReply = msg
				continue
			}
			if !interrupted {
				idle.reset(opts.IdleTimeout)
			}
			reply = msg
			break executeLoop
		}
	}

	if interrupted {
		return nil, ctx.Err()
	}

	content := reply.contentMap()
	result := &Result{Outputs: acc.Outputs(), Status: str(content["status"])}
	if v, ok := content["execution_count"].(float64); ok {
		n := int(v)
		result.ExecutionCount = &n
	}
	if result.Status == "error" && !hasErrorOutput(result.Outputs) {
		trace, _ := content["traceback"].([]any)
		lines := make([]string, 0, len(trace))
		for _, t := range trace {
			lines = append(lines, fmt.Sprint(t))
		}
		ename, evalue := str(content["ename"]), str(content["evalue"])
		if ename == "" {
			ename = "Error"
		}
		if evalue == "" {
			evalue = "Unknown error"
		}
		result.Outputs = append(result.Outputs, Output{
			OutputType: "error", EName: ename, EValue: evalue, Traceback: lines,
		})
	}
	return result, nil
}

// acceptMessage runs the terminal-status check BEFORE the parent filter, then
// drops messages from other requests.
func acceptMessage(msg *Message, msgID string) (bool, error) {
	if msg.Header.MsgType == "status" {
		if state, _ := msg.contentMap()["execution_state"].(string); state == "dead" || state == "restarting" {
			return false, &KernelGoneError{State: state}
		}
	}
	return msg.ParentHeader.MsgID == msgID, nil
}

func hasErrorOutput(outputs []Output) bool {
	for _, o := range outputs {
		if o.OutputType == "error" {
			return true
		}
	}
	return false
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (k *kernel) Restart(ctx context.Context) error {
	return k.rest.lifecycle(ctx, "restart", k.id)
}

func (k *kernel) Interrupt(ctx context.Context) error {
	return k.rest.lifecycle(ctx, "interrupt", k.id)
}

func (k *kernel) Shutdown(ctx context.Context) error {
	return k.rest.lifecycle(ctx, "shutdown", k.id)
}

func (k *kernel) Close() error {
	k.closeOnce.Do(func() { k.conn.close() })
	return nil
}

// SendColabReply answers an intercepted colab_request on the stdin channel.
func (k *kernel) SendColabReply(ctx context.Context, req Request, body any) error {
	if body == nil {
		body = req.DefaultReplyBody()
	}
	reply := newMessage("input_reply", "stdin", k.sessionID, req.Header,
		map[string]any{"value": body})
	return k.conn.send(ctx, reply)
}

// stdinLoop drains the stdin channel and answers input_request messages. It is
// independent of the execute loop so streaming output and stdin can be used
// together (ADR-009.5, upstream forbids this combination).
func (k *kernel) stdinLoop() {
	for {
		select {
		case <-k.conn.done:
			return
		case msg := <-k.conn.ch("stdin"):
			if msg.Header.MsgType != "input_request" {
				continue
			}
			k.conn.activeMu.RLock()
			active := k.conn.active
			k.conn.activeMu.RUnlock()
			if active.msgID == "" || !active.opts.AllowStdin || active.opts.StdinHook == nil {
				continue
			}
			if parent := msg.ParentHeader.MsgID; parent != "" && parent != active.msgID {
				continue
			}
			prompt, _ := msg.contentMap()["prompt"].(string)
			value, err := active.opts.StdinHook(prompt)
			if err != nil {
				value = ""
				k.logger.Debug("stdin hook failed", "error", err)
			}
			reply := newMessage("input_reply", "stdin", active.session, msg.Header,
				map[string]any{"value": value})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := k.conn.send(ctx, reply); err != nil && !errors.Is(err, context.Canceled) {
				k.logger.Debug("could not send input_reply", "error", err)
			}
			cancel()
		}
	}
}
