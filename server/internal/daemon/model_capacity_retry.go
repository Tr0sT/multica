package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Retry inside the task so its slot and local-directory lock stay held while
// the model is unavailable and the working directory may contain partial work.
func (d *Daemon) executeWithModelCapacityRetry(ctx context.Context, backend agent.Backend, prompt string, opts agent.ExecOptions, taskLog *slog.Logger, taskID, codexHome string, msgSeq *atomic.Int32) (agent.Result, int32, error) {
	timeout := opts.Timeout
	deadline := time.Now().Add(timeout)
	result, tools, err := d.executeAndDrain(ctx, backend, prompt, opts, taskLog, taskID, codexHome, msgSeq)
	if err != nil {
		return result, tools, err
	}
	for attempt := 1; result.Status == "failed" &&
		strings.Contains(strings.ToLower(result.Error), "model is at capacity") &&
		taskfailure.Classify(result.Error) == taskfailure.ReasonAgentProviderCapacityOrRateLimit; attempt++ {
		if ctx.Err() != nil {
			result.Status = "cancelled"
			result.Error = ctx.Err().Error()
			break
		}
		if timeout > 0 {
			opts.Timeout = time.Until(deadline)
			if opts.Timeout <= 0 {
				result.Status = "timeout"
				result.Error = fmt.Sprintf("agent execution timed out after %s", timeout)
				break
			}
		}
		if result.SessionID != "" {
			opts.ResumeSessionID = result.SessionID
		}
		if opts.ResumeSessionID != "" {
			opts.ResumeExpected = true
			prompt = "Continue the interrupted task from this session and working directory. The previous attempt stopped because the model was at capacity. Preserve existing changes and do not repeat completed actions."
		}
		taskLog.Warn("model at capacity; retrying in the same task", "attempt", attempt, "session_id", opts.ResumeSessionID)
		retry, retryTools, retryErr := d.executeAndDrain(ctx, backend, prompt, withPriorTaskUsage(opts, result.Usage), taskLog, taskID, codexHome, msgSeq)
		if retryErr != nil {
			// Keep the prior session and usage even if the next process cannot start.
			result.Error = retryErr.Error()
			return result, tools, nil
		}
		if retry.SessionID == "" {
			retry.SessionID = result.SessionID
		}
		retry.Usage = mergeUsage(result.Usage, retry.Usage)
		result = retry
		tools += retryTools
	}
	return result, tools, nil
}
