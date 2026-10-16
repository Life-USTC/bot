package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// Run finalization must still be able to close a started row when its
	// caller cancels, but it must not turn cleanup into another unbounded run.
	agentRunCleanupTimeout = 5 * time.Second

	// Each logical model request may retry its network operation independently.
	// This is deliberately per request; it is not a budget for the complete
	// conversation or tool loop.
	//
	// The budget is sized against the slowest legitimate request rather than a
	// typical one. History compaction sends a large prefix in a single call and
	// routinely runs for one to four minutes, so a window that expires in
	// seconds gives up while the provider is merely slow or briefly refusing
	// connections, and discards the user's turn with it.
	llmRequestMaxAttempts = 8
)

var (
	errAgentNonProgress    = errors.New("agent tool plan made no progress")
	errLLMUpstreamCanceled = errors.New("llm upstream canceled request")
)

type runMetricsContextKey struct{}
type modelAttemptRecorderContextKey struct{}

// runMetrics contains only low-cardinality aggregate values. It deliberately
// never stores prompts, tool arguments, provider responses, or user IDs.
type runMetrics struct {
	mu sync.Mutex

	stageMilliseconds map[string]int64
	contextTokens     int64
	modelRequests     int64
	toolCalls         int64
}

type runMetricsSnapshot struct {
	contextTokens     int64
	modelRequests     int64
	toolCalls         int64
	stageMilliseconds map[string]int64
}

func newRunMetrics() *runMetrics {
	return &runMetrics{stageMilliseconds: make(map[string]int64)}
}

func withRunMetrics(ctx context.Context, metrics *runMetrics) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runMetricsContextKey{}, metrics)
}

func runMetricsFromContext(ctx context.Context) *runMetrics {
	if ctx == nil {
		return nil
	}
	if metrics, ok := ctx.Value(runMetricsContextKey{}).(*runMetrics); ok {
		return metrics
	}
	return nil
}

func (m *runMetrics) addStage(stage string, duration time.Duration) {
	if m == nil || stage == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stageMilliseconds == nil {
		m.stageMilliseconds = make(map[string]int64)
	}
	m.stageMilliseconds[stage] += duration.Milliseconds()
}

func (m *runMetrics) recordModelRequest(contextTokens int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelRequests++
	if contextTokens > m.contextTokens {
		m.contextTokens = contextTokens
	}
}

func (m *runMetrics) recordToolCall() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.toolCalls++
	m.mu.Unlock()
}

func (m *runMetrics) snapshot() runMetricsSnapshot {
	if m == nil {
		return runMetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stages := make(map[string]int64, len(m.stageMilliseconds))
	for name, duration := range m.stageMilliseconds {
		stages[name] = duration
	}
	return runMetricsSnapshot{
		contextTokens:     m.contextTokens,
		modelRequests:     m.modelRequests,
		toolCalls:         m.toolCalls,
		stageMilliseconds: stages,
	}
}

func withModelAttemptRecorder(ctx context.Context, recorder func(context.Context) error) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, modelAttemptRecorderContextKey{}, recorder)
}

func modelAttemptRecorderFromContext(ctx context.Context) func(context.Context) error {
	if ctx == nil {
		return nil
	}
	if recorder, ok := ctx.Value(modelAttemptRecorderContextKey{}).(func(context.Context) error); ok {
		return recorder
	}
	return nil
}

func callerContextError(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return ctx.Err()
}

// admitModelRequest records one logical model request after checking only the
// caller context. Provider context capacity is enforced by the provider; this
// layer does not guess a model window or impose a run-wide token budget.
func admitModelRequest(ctx context.Context, contextTokens int64) error {
	return admitModelAttemptWithContext(ctx, contextTokens)
}

// admitModelAttempt records one physical provider HTTP attempt. It remains
// separate from admitModelRequest so each retry is visible in usage metrics,
// while no aggregate attempt ceiling can terminate an otherwise live run.
func admitModelAttempt(ctx context.Context) error {
	return admitModelAttemptWithContext(ctx, 1)
}

func admitModelAttemptWithContext(ctx context.Context, contextTokens int64) error {
	if err := callerContextError(ctx); err != nil {
		return err
	}
	if recorder := modelAttemptRecorderFromContext(ctx); recorder != nil {
		if err := recorder(ctx); err != nil {
			return err
		}
	}
	if metrics := runMetricsFromContext(ctx); metrics != nil {
		metrics.recordModelRequest(contextTokens)
	}
	return nil
}

func admitToolCall(ctx context.Context) error {
	if err := callerContextError(ctx); err != nil {
		return err
	}
	if metrics := runMetricsFromContext(ctx); metrics != nil {
		metrics.recordToolCall()
	}
	return nil
}

func recordRunStage(ctx context.Context, stage string, duration time.Duration) {
	if metrics := runMetricsFromContext(ctx); metrics != nil {
		metrics.addStage(stage, duration)
	}
}

func observeRunStage(ctx context.Context, stage string, work func() error) error {
	started := time.Now()
	defer func() { recordRunStage(ctx, stage, time.Since(started)) }()
	return work()
}

func observeRunStageValue[T any](ctx context.Context, stage string, work func() (T, error)) (T, error) {
	started := time.Now()
	defer func() { recordRunStage(ctx, stage, time.Since(started)) }()
	return work()
}

func agentFailureClass(err error) string {
	switch {
	case errors.Is(err, errAgentNonProgress):
		return "non_progress"
	case errors.Is(err, errLLMTransportExhausted):
		return "upstream_transport"
	case errors.Is(err, errLLMUpstreamCanceled):
		return "upstream_canceled"
	case errors.Is(err, errRepeatedToolCall):
		return "repeated_tool_call"
	case isTimeoutError(err):
		return "request_timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errConversationCompaction):
		return "history_compaction"
	default:
		return "internal"
	}
}

func normalizeAgentRunError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
			return context.Canceled
		} else if errors.Is(ctxErr, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
	}
	if errors.Is(err, context.Canceled) {
		return errLLMUpstreamCanceled
	}
	return err
}
