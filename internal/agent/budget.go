package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// The run deadline is intentionally fixed: it matches the webhook's
	// existing 90-second dispatch lifetime and also bounds gateway-triggered
	// runs that do not have an HTTP request context.
	agentRunDeadline = 90 * time.Second

	// Run finalization must still be able to close a started row when its
	// caller cancels, but it must not turn cleanup into another unbounded run.
	agentRunCleanupTimeout = 5 * time.Second

	// Keep one bounded budget for prompt input plus the existing Kimi output
	// ceiling. conversationCompactInputLimit is the largest provider input
	// budget already used by history compaction; adding the known 8,192-token
	// completion ceiling leaves no room for an unbounded sequence of retries.
	agentRunTokenBudget  int64 = conversationCompactInputLimit + kimiMaxCompletionTokens
	agentRunMaxToolCalls       = 12
)

var (
	errAgentRunDeadline    = errors.New("agent run deadline exceeded")
	errAgentContextBudget  = errors.New("agent context budget exceeded")
	errAgentToolCallBudget = errors.New("agent tool-call budget exceeded")
	errAgentNonProgress    = errors.New("agent tool plan made no progress")
)

type runBudgetContextKey struct{}
type runMetricsContextKey struct{}

// runMetrics contains only low-cardinality aggregate values. It deliberately
// never stores prompts, tool arguments, provider responses, or user IDs.
type runMetrics struct {
	mu sync.Mutex

	stageMilliseconds map[string]int64
	contextTokens     int64
	compactionMs      int64
	modelRequests     int64
	toolCalls         int64
}

type runMetricsSnapshot struct {
	contextTokens     int64
	compactionMs      int64
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

func (m *runMetrics) recordCompaction(duration time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.compactionMs += duration.Milliseconds()
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
		compactionMs:      m.compactionMs,
		modelRequests:     m.modelRequests,
		toolCalls:         m.toolCalls,
		stageMilliseconds: stages,
	}
}

type runBudget struct {
	mu sync.Mutex

	deadline       time.Time
	reservedTokens int64
	toolCalls      int
	metrics        *runMetrics
}

func newRunBudget(now time.Time, metrics *runMetrics) *runBudget {
	return &runBudget{
		deadline: now.Add(agentRunDeadline),
		metrics:  metrics,
	}
}

func withRunBudget(ctx context.Context, budget *runBudget) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runBudgetContextKey{}, budget)
}

func runBudgetFromContext(ctx context.Context) *runBudget {
	if ctx == nil {
		return nil
	}
	if budget, ok := ctx.Value(runBudgetContextKey{}).(*runBudget); ok {
		return budget
	}
	return nil
}

func (b *runBudget) contextError(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if b != nil && !b.deadline.After(time.Now()) && errors.Is(ctxErr, context.DeadlineExceeded) {
			return errAgentRunDeadline
		}
		return ctxErr
	}
	if b != nil && !b.deadline.After(time.Now()) {
		return errAgentRunDeadline
	}
	return nil
}

// admitModelRequest reserves the estimated input plus the existing provider
// completion ceiling before an HTTP attempt. Retries call this separately, so
// they consume both time and cumulative token budget.
func admitModelRequest(ctx context.Context, contextTokens int64) error {
	budget := runBudgetFromContext(ctx)
	if budget == nil {
		return nil
	}
	if err := budget.contextError(ctx); err != nil {
		return err
	}
	if contextTokens < 1 {
		contextTokens = 1
	}
	reservation := contextTokens + int64(kimiMaxCompletionTokens)
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.reservedTokens > agentRunTokenBudget-reservation {
		return errAgentContextBudget
	}
	budget.reservedTokens += reservation
	if budget.metrics != nil {
		budget.metrics.recordModelRequest(contextTokens)
	}
	return nil
}

func admitToolCall(ctx context.Context) error {
	budget := runBudgetFromContext(ctx)
	if budget == nil {
		return nil
	}
	if err := budget.contextError(ctx); err != nil {
		return err
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.toolCalls >= agentRunMaxToolCalls {
		return errAgentToolCallBudget
	}
	budget.toolCalls++
	if budget.metrics != nil {
		budget.metrics.recordToolCall()
	}
	return nil
}

func recordRunStage(ctx context.Context, stage string, duration time.Duration) {
	if metrics := runMetricsFromContext(ctx); metrics != nil {
		metrics.addStage(stage, duration)
	}
}

func recordRunCompaction(ctx context.Context, duration time.Duration) {
	if metrics := runMetricsFromContext(ctx); metrics != nil {
		metrics.recordCompaction(duration)
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
	case errors.Is(err, errAgentRunDeadline):
		return "run_deadline"
	case errors.Is(err, errAgentContextBudget):
		return "context_budget"
	case errors.Is(err, errAgentToolCallBudget):
		return "tool_call_budget"
	case errors.Is(err, errAgentNonProgress):
		return "non_progress"
	case errors.Is(err, errRepeatedToolCall):
		return "repeated_tool_call"
	case isAgentIterationLimitError(err):
		return "iteration_limit"
	case isTimeoutError(err):
		return "request_timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "internal"
	}
}

func isAgentBudgetError(err error) bool {
	return errors.Is(err, errAgentRunDeadline) ||
		errors.Is(err, errAgentContextBudget) ||
		errors.Is(err, errAgentToolCallBudget) ||
		errors.Is(err, errAgentNonProgress)
}

func normalizeAgentRunError(ctx context.Context, budget *runBudget, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAgentRunDeadline) ||
		errors.Is(err, errAgentContextBudget) ||
		errors.Is(err, errAgentToolCallBudget) ||
		errors.Is(err, errAgentNonProgress) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) && budget != nil &&
		!budget.deadline.After(time.Now()) {
		return errAgentRunDeadline
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.Canceled) {
			return context.Canceled
		} else if errors.Is(ctxErr, context.DeadlineExceeded) && budget != nil &&
			!budget.deadline.After(time.Now()) {
			return errAgentRunDeadline
		}
	}
	return err
}
