package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const modelCapacityError = "Selected model is at capacity. Please try a different model."

type modelCapacityBackend func(context.Context, string, agent.ExecOptions) (*agent.Session, error)

func (f modelCapacityBackend) Execute(ctx context.Context, prompt string, opts agent.ExecOptions) (*agent.Session, error) {
	return f(ctx, prompt, opts)
}

func TestModelCapacityRetryContinuesPartialWork(t *testing.T) {
	d, transcript := newTranscriptRecorder(t)
	workdir := t.TempDir()
	partial := filepath.Join(workdir, "partial.txt")
	calls := 0
	var liveUsage []int64
	previousTimeout := time.Minute
	backend := modelCapacityBackend(func(_ context.Context, prompt string, opts agent.ExecOptions) (*agent.Session, error) {
		calls++
		if opts.Cwd != workdir || opts.Model != "selected-model" {
			t.Fatalf("retry changed working directory or model: %+v", opts)
		}
		if calls == 1 {
			if err := os.WriteFile(partial, []byte("unfinished work"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			if opts.ResumeSessionID != "capacity-session" || !opts.ResumeExpected {
				t.Fatalf("retry lost the active session: %+v", opts)
			}
			if prompt == "original task" || !strings.Contains(prompt, "do not repeat completed actions") {
				t.Fatalf("retry must continue prior work, got prompt %q", prompt)
			}
			if data, err := os.ReadFile(partial); err != nil || string(data) != "unfinished work" {
				t.Fatalf("partial work changed: %q, %v", data, err)
			}
			if opts.Timeout <= 0 || opts.Timeout >= previousTimeout {
				t.Fatalf("retry reset the timeout: %s after %s", opts.Timeout, previousTimeout)
			}
		}
		previousTimeout = opts.Timeout
		messages := make(chan agent.Message, 2)
		callID := fmt.Sprintf("tool-%d", calls)
		messages <- agent.Message{Type: agent.MessageToolUse, Tool: "edit", CallID: callID}
		messages <- agent.Message{Type: agent.MessageToolResult, Tool: "edit", CallID: callID, Output: "saved"}
		close(messages)
		results := make(chan agent.Result, 1)
		result := agent.Result{
			Status: "failed", Error: modelCapacityError, SessionID: "capacity-session",
			Usage: map[string]agent.TokenUsage{"selected-model": {InputTokens: 10}},
		}
		if calls == 6 {
			result.Status, result.Error = "completed", ""
		}
		opts.OnUsage(result.Usage)
		results <- result
		close(results)
		return &agent.Session{Messages: messages, Result: results}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	opts := withPriorTaskUsage(agent.ExecOptions{
		Cwd: workdir, Model: "selected-model", Timeout: time.Minute,
		OnUsage: func(usage map[string]agent.TokenUsage) {
			liveUsage = append(liveUsage, usage["selected-model"].InputTokens)
		},
	}, map[string]agent.TokenUsage{"selected-model": {InputTokens: 100}})
	result, tools, err := d.executeWithModelCapacityRetry(ctx, backend, "original task", opts, slog.Default(), "same-task", "", new(atomic.Int32))
	if err != nil || result.Status != "completed" || calls != 6 || tools != 6 {
		t.Fatalf("result=%+v calls=%d tools=%d err=%v, want success after five immediate retries", result, calls, tools, err)
	}
	if result.Usage["selected-model"].InputTokens != 60 {
		t.Fatalf("lost usage from interrupted attempts: %+v", result.Usage)
	}
	if len(liveUsage) != calls {
		t.Fatalf("live usage updates=%d, want %d", len(liveUsage), calls)
	}
	for i, usage := range liveUsage {
		if want := int64(100 + (i+1)*10); usage != want {
			t.Fatalf("attempt %d reported %d tokens, want %d including prior turns", i+1, usage, want)
		}
	}
	messages := transcript.snapshot()
	if len(messages) != 12 {
		t.Fatalf("transcript has %d messages, want 12", len(messages))
	}
	for i, message := range messages {
		if message.Seq != i+1 {
			t.Fatalf("retry reset transcript sequence: %+v", messages)
		}
	}
}

func TestModelCapacityRetryLeavesOtherFailuresTerminal(t *testing.T) {
	for _, message := range []string{
		"The selected model is no longer supported",
		"HTTP 403: " + modelCapacityError,
		"Quota exceeded: " + modelCapacityError,
		"API Error: 429 Too Many Requests",
		agent.CodexGuardianRateLimitMarker,
	} {
		t.Run(message, func(t *testing.T) {
			d := newTestDaemon(t)
			backend := &fakeBackend{results: []agent.Result{{Status: "failed", Error: message}}}
			result, _, err := d.executeWithModelCapacityRetry(t.Context(), backend, "task", agent.ExecOptions{}, slog.Default(), "task", "", new(atomic.Int32))
			if err != nil || result.Error != message || len(backend.calls) != 1 {
				t.Fatalf("unrelated failure retried: result=%+v calls=%d err=%v", result, len(backend.calls), err)
			}
		})
	}
}

func TestModelCapacityRetryStopsOnCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(fmt.Sprint("cancelled=", cancelled), func(t *testing.T) {
			d := newTestDaemon(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &fakeBackend{results: []agent.Result{{Status: "failed", Error: modelCapacityError, SessionID: "session"}}}
			backend := modelCapacityBackend(func(ctx context.Context, prompt string, opts agent.ExecOptions) (*agent.Session, error) {
				if cancelled {
					cancel()
				}
				return fake.Execute(ctx, prompt, opts)
			})
			opts := agent.ExecOptions{}
			wantStatus := "cancelled"
			if !cancelled {
				opts.Timeout = time.Nanosecond
				wantStatus = "timeout"
			}
			result, _, err := d.executeWithModelCapacityRetry(ctx, backend, "task", opts, slog.Default(), "task", "", new(atomic.Int32))
			if err != nil || result.Status != wantStatus || len(fake.calls) != 1 {
				t.Fatalf("result=%+v calls=%d err=%v, want %s without another launch", result, len(fake.calls), err, wantStatus)
			}
		})
	}
}

func TestModelCapacityRetryPreservesUsageWhenNextLaunchFails(t *testing.T) {
	d := newTestDaemon(t)
	backend := &fakeBackend{
		results: []agent.Result{{Status: "failed", Error: modelCapacityError, SessionID: "session", Usage: map[string]agent.TokenUsage{"model": {InputTokens: 10}}}},
		errors:  []error{nil, errors.New("executable missing")},
	}
	result, _, err := d.executeWithModelCapacityRetry(t.Context(), backend, "task", agent.ExecOptions{}, slog.Default(), "task", "", new(atomic.Int32))
	if err != nil || result.Status != "failed" || result.Error != "executable missing" || result.SessionID != "session" || result.Usage["model"].InputTokens != 10 {
		t.Fatalf("lost prior work after launch failure: result=%+v err=%v", result, err)
	}
}
