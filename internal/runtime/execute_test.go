package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/20Youssef10/golab/internal/testutil/fakekernel"
)

func runExec(t *testing.T, k Kernel, code string, opts ExecOptions) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return k.Execute(ctx, code, opts)
}

// waitInputs waits for the fake to observe n input_reply frames. A successful
// WebSocket write only means the frame was handed to the kernel, not that the
// server has dispatched it yet.
func waitInputs(t *testing.T, srv *fakekernel.Server, n int) []fakekernel.InputRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := srv.Inputs(); len(got) >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d input replies", n)
	return nil
}

func TestExecuteStreaming(t *testing.T) {
	// Ports test_runtime_execute_code_streaming: each new output is streamed
	// exactly once, adjacent streams are not merged, code/allow_stdin forwarded.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Stream("stdout", "1\n"),
		fakekernel.Stream("stderr", "2\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	var streamed []Output
	res, err := runExec(t, k, "print(1); print(2)", ExecOptions{OnOutput: func(o Output) { streamed = append(streamed, o) }})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Outputs) != 2 || res.Outputs[0].Text != "1\n" || res.Outputs[1].Text != "2\n" {
		t.Fatalf("outputs: %+v", res.Outputs)
	}
	if len(streamed) != 2 || streamed[1].Name != "stderr" {
		t.Fatalf("streamed: %+v", streamed)
	}
	execs := srv.Executions()
	if len(execs) != 1 || execs[0].Code != "print(1); print(2)" || execs[0].AllowStdin {
		t.Fatalf("execute_request wrong: %+v", execs)
	}
	if res.Status != "ok" {
		t.Fatalf("result status %q", res.Status)
	}
}

func TestExecuteIdleTimeoutIsPerMessage(t *testing.T) {
	// Ports test_silence_longer_than_the_timeout_raises (and
	// test_a_run_that_keeps_printing_is_not_cut_off): the idle limit resets when
	// a message from THIS execution arrives, not at the start of the call.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Step{Delay: 60 * time.Millisecond, MsgType: "stream", Content: map[string]any{"name": "stdout", "text": "early"}},
		fakekernel.Step{Delay: 160 * time.Millisecond, MsgType: "status", Content: map[string]any{"execution_state": "idle"}},
		fakekernel.Reply("ok", nil),
	)
	_, err = runExec(t, k, "work()", ExecOptions{IdleTimeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	var te *TimeoutError
	if !errors.As(err, &te) || !strings.Contains(te.Error(), "--timeout") {
		t.Fatalf("want actionable TimeoutError, got %#v", err)
	}
}

func TestExecuteNoIdleLimit(t *testing.T) {
	// Ports test_no_limit_waits_through_any_silence: 0 means unlimited.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Step{Delay: 60 * time.Millisecond, MsgType: "stream", Content: map[string]any{"name": "stdout", "text": "done\n"}},
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	res, err := runExec(t, k, "work()", ExecOptions{IdleTimeout: 0})
	if err != nil {
		t.Fatalf("Execute with no limit: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Text != "done\n" {
		t.Fatalf("outputs: %+v", res.Outputs)
	}
}

func TestExecuteDeadKernelBeforeParentFilter(t *testing.T) {
	// Ports test_a_kernel_that_dies_or_restarts_ends_the_wait for both states.
	for _, state := range []string{"dead", "restarting"} {
		t.Run(state, func(t *testing.T) {
			srv := fakekernel.New(t)
			k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer k.Close()
			dead := fakekernel.Status(state)
			dead.Parent = "none" // announced to every client, without our parent
			srv.Script(dead, fakekernel.Status("idle"), fakekernel.Reply("ok", nil))
			_, err = runExec(t, k, "work()", ExecOptions{})
			if !errors.Is(err, ErrKernelGone) {
				t.Fatalf("want ErrKernelGone, got %v", err)
			}
			if !strings.Contains(err.Error(), state) {
				t.Fatalf("error should name the state: %v", err)
			}
		})
	}
}

func TestExecuteErrorSynthesis(t *testing.T) {
	// Ports test_runtime_execute_code_streaming_error_synthesis: a reply with
	// status=error but no error iopub must produce a synthesized error output,
	// which is returned but not streamed.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(fakekernel.Status("idle"), fakekernel.Reply("error", map[string]any{
		"ename": "RuntimeError", "evalue": "boom", "traceback": []string{"tb"},
	}))
	var streamed []Output
	res, err := runExec(t, k, "fail()", ExecOptions{OnOutput: func(o Output) { streamed = append(streamed, o) }})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].OutputType != "error" ||
		res.Outputs[0].EName != "RuntimeError" || res.Outputs[0].EValue != "boom" {
		t.Fatalf("synthesized error: %+v", res.Outputs)
	}
	if len(streamed) != 0 {
		t.Fatalf("synthesized error must not be streamed: %+v", streamed)
	}
}

func TestExecuteIgnoresOtherRequests(t *testing.T) {
	// Ports test_messages_from_other_requests_are_ignored and
	// test_output_from_other_requests_does_not_reset_the_timeout.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	stale := fakekernel.Stream("stdout", "other\n")
	stale.Parent = "req-0"
	srv.Script(
		stale,
		fakekernel.Stream("stdout", "ours\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	res, err := runExec(t, k, "work()", ExecOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Text != "ours\n" {
		t.Fatalf("stale message was not ignored: %+v", res.Outputs)
	}

	// A stream of stale messages must not keep a dead execution alive.
	srv.Script(
		staleWithDelay(stale, 10*time.Millisecond),
		staleWithDelay(stale, 20*time.Millisecond),
		staleWithDelay(stale, 30*time.Millisecond),
		staleWithDelay(stale, 40*time.Millisecond),
		staleWithDelay(stale, 50*time.Millisecond),
	)
	start := time.Now()
	_, err = runExec(t, k, "work()", ExecOptions{IdleTimeout: 25 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("stale messages reset the timeout: took %v", time.Since(start))
	}
}

func staleWithDelay(s fakekernel.Step, d time.Duration) fakekernel.Step {
	s.Delay = d
	return s
}

func TestExecuteStdinAndStreamingTogether(t *testing.T) {
	// Ports test_colab_runtime_stdin_logging + ADR-009.5: streaming output and
	// stdin are compatible (upstream forbids the combination).
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Stream("stdout", "before\n"),
		fakekernel.InputRequest("name? "),
		fakekernel.Stream("stdout", "after\n"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	var streams []string
	prompts := make(chan string, 1)
	res, err := runExec(t, k, "input()", ExecOptions{
		AllowStdin: true,
		OnOutput:   func(o Output) { streams = append(streams, o.Text) },
		StdinHook: func(prompt string) (string, error) {
			prompts <- prompt
			return "alice", nil
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Outputs) != 2 || strings.Join(streams, "") != "before\nafter\n" {
		t.Fatalf("outputs/streams: %+v / %v", res.Outputs, streams)
	}
	if p := <-prompts; p != "name? " {
		t.Fatalf("prompt %q", p)
	}
	inputs := waitInputs(t, srv, 1)
	var value string
	if err := json.Unmarshal(inputs[0].Value, &value); err != nil || value != "alice" {
		t.Fatalf("input_reply value: %s (%v)", inputs[0].Value, err)
	}
}

func TestColabRequestReplyRoundTrip(t *testing.T) {
	// Ports the drivemount flow in upstream commands/automation.py §_send_colab_reply:
	// the hook handles the request off the reader goroutine and sends an
	// input_reply on stdin whose parent_header is the colab_request header.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.ColabRequest("dfs_ephemeral", "cm-42"),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	gotReq := make(chan Request, 1)
	replyErr := make(chan error, 1)
	var req Request
	_, err = runExec(t, k, "from google.colab import drive; drive.mount('/drive')", ExecOptions{
		ColabRequestHook: func(r Request) bool {
			gotReq <- r
			replyErr <- k.SendColabReply(context.Background(), r, nil)
			return true
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	req = <-gotReq
	if err := <-replyErr; err != nil {
		t.Fatalf("SendColabReply: %v", err)
	}
	if req.AuthType != "dfs_ephemeral" || req.ColabMsgID != "cm-42" || req.MsgID == "" {
		t.Fatalf("request not parsed: %+v", req)
	}
	inputs := waitInputs(t, srv, 1)
	var body map[string]any
	if err := json.Unmarshal(inputs[0].Value, &body); err != nil {
		t.Fatalf("colab_reply is not JSON: %s", inputs[0].Value)
	}
	if body["type"] != "colab_reply" || body["colab_msg_id"] != "cm-42" {
		t.Fatalf("wrong colab_reply body: %v", body)
	}
	if inputs[0].ParentHeader["msg_id"] != req.MsgID {
		t.Fatalf("parent_header not the request header: %v vs %s", inputs[0].ParentHeader, req.MsgID)
	}
	if inputs[0].MsgType != "input_reply" {
		t.Fatalf("reply msg_type %q", inputs[0].MsgType)
	}
}

func TestExecuteBinaryBuffersAndLargeMessage(t *testing.T) {
	// Ports the "binary buffers" and "10 MB messages" fakekernel scenarios.
	srv := fakekernel.New(t)
	k, err := newTestRuntime(t, srv).Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Step{MsgType: "display_data", Buffers: [][]byte{[]byte("png-bytes")},
			Content: map[string]any{"data": map[string]any{"image/png": "AAAA"}, "metadata": map[string]any{}}},
		fakekernel.BigStream(10<<20),
		fakekernel.Status("idle"),
		fakekernel.Reply("ok", nil),
	)
	res, err := runExec(t, k, "plot()", ExecOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Outputs) != 2 || len(res.Outputs[1].Text) != 10<<20 {
		t.Fatalf("large message was truncated: %d outputs, second len=%d", len(res.Outputs), len(res.Outputs[1].Text))
	}
}

func TestExecuteCancelWaitsForIdleWithoutForceClose(t *testing.T) {
	srv := fakekernel.New(t)
	rt, err := New(Config{HTTPClient: srv.Client(), GracefulCancelTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	k, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	srv.Script(
		fakekernel.Step{Delay: 10 * time.Millisecond, MsgType: "stream", Content: map[string]any{"name": "stdout", "text": "working"}},
		fakekernel.Step{Delay: 100 * time.Millisecond, MsgType: "status", Content: map[string]any{"execution_state": "idle"}},
		fakekernel.Step{Delay: 100 * time.Millisecond, Channel: "shell", MsgType: "execute_reply", Content: map[string]any{"status": "ok"}},
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	_, err = k.Execute(ctx, "long()", ExecOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	// The connection must survive: only a second cancellation force-closes it.
	if _, err := k.Execute(context.Background(), "after()", ExecOptions{}); err != nil {
		t.Fatalf("connection was force-closed after a single cancel: %v", err)
	}
}

func TestExecuteContextCancelInterrupts(t *testing.T) {
	srv := fakekernel.New(t)
	rt, err := New(Config{HTTPClient: srv.Client(), GracefulCancelTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	k, err := rt.Attach(context.Background(), target(srv, ""), AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	// No idle status: the kernel stays silent so cancellation must interrupt it.
	srv.Script(fakekernel.Stream("stdout", "working\n"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	_, err = k.Execute(ctx, "long()", ExecOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	interrupted := false
	for _, r := range srv.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/interrupt") {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatal("cancellation did not send REST interrupt")
	}
}
