package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Keep a single deadline for every stage of one Agent run, including all
	// model requests and tool calls. It also bounds gateway-triggered runs that
	// do not have an HTTP request context.
	agentRunDeadline = 60 * time.Second

	// Run finalization must still be able to close a started row when its
	// caller cancels, but it must not turn cleanup into another unbounded run.
	agentRunCleanupTimeout = 5 * time.Second

	// Keep one bounded budget for logical prompt input plus the existing Kimi
	// output ceiling. conversationCompactInputLimit is the hard provider input
	// budget; physical retries reuse their logical request's reservation instead
	// of consuming it again.
	agentRunTokenBudget  int64 = conversationCompactInputLimit + kimiMaxCompletionTokens
	agentRunMaxToolCalls       = 12
	// A tool loop can make at most one more logical model request than tool
	// calls. Each logical request receives its own bounded retry window; the
	// aggregate durable limit prevents a restart from resetting that budget.
	llmRequestMaxAttempts    = 5
	agentRunMaxModelAttempts = (agentRunMaxToolCalls + 1) * llmRequestMaxAttempts
)

var (
	errAgentRunDeadline        = errors.New("agent run deadline exceeded")
	errAgentContextBudget      = errors.New("agent context budget exceeded")
	errAgentModelAttemptBudget = errors.New("agent model-attempt budget exceeded")
	errAgentToolCallBudget     = errors.New("agent tool-call budget exceeded")
	errAgentNonProgress        = errors.New("agent tool plan made no progress")
)

type runBudgetContextKey struct{}
type runMetricsContextKey struct{}

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

type runBudget struct {
	mu sync.Mutex

	deadline       time.Time
	reservedTokens int64
	modelAttempts  atomic.Int32
	toolCalls      int
	metrics        *runMetrics
	// reserveModelAttempt is installed for conversation jobs after their
	// started run row is created. It durably reserves the physical attempt
	// before the transport calls the provider.
	reserveModelAttempt func(context.Context) (bool, error)
}

func newRunBudget(now time.Time, metrics *runMetrics) *runBudget {
	return newRunBudgetWithAttempts(now, metrics, 0)
}

func newRunBudgetWithAttempts(now time.Time, metrics *runMetrics, priorModelAttempts int64) *runBudget {
	budget := &runBudget{
		deadline: now.Add(agentRunDeadline),
		metrics:  metrics,
	}
	if priorModelAttempts > int64(agentRunMaxModelAttempts) {
		priorModelAttempts = int64(agentRunMaxModelAttempts)
	}
	if priorModelAttempts > 0 {
		budget.modelAttempts.Store(int32(priorModelAttempts))
	}
	return budget
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

// contextWithRunDeadline gives a request the run budget's deadline when its
// caller did not already provide an earlier one. The returned cancel function
// is always safe to call.
func contextWithRunDeadline(ctx context.Context, budget *runBudget) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if budget == nil || budget.deadline.IsZero() {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && !budget.deadline.Before(deadline) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, budget.deadline)
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
// completion ceiling and admits the first physical provider attempt for one
// logical model request. Retries must use admitModelAttempt: they count
// against the run-wide attempt limit but do not reserve the same prompt and
// completion budget again.
func admitModelRequest(ctx context.Context, contextTokens int64) error {
	budget := runBudgetFromContext(ctx)
	if budget == nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if metrics := runMetricsFromContext(ctx); metrics != nil {
			metrics.recordModelRequest(contextTokens)
		}
		return nil
	}
	if err := budget.contextError(ctx); err != nil {
		return err
	}
	if contextTokens < 1 {
		contextTokens = 1
	}
	completionBudget := int64(kimiMaxCompletionTokens)
	if contextTokens > agentRunTokenBudget-completionBudget {
		return errAgentContextBudget
	}
	reservation := contextTokens + completionBudget
	budget.mu.Lock()
	if err := budget.contextError(ctx); err != nil {
		budget.mu.Unlock()
		return err
	}
	if budget.reservedTokens > agentRunTokenBudget-reservation {
		budget.mu.Unlock()
		return errAgentContextBudget
	}
	budget.reservedTokens += reservation
	budget.mu.Unlock()

	if err := admitModelAttemptWithContext(ctx, contextTokens); err != nil {
		// A race can consume the last attempt slot after the reservation was
		// made. A rejected physical attempt must not leave that reservation
		// behind and reduce the budget available to later logical requests.
		budget.mu.Lock()
		budget.reservedTokens -= reservation
		budget.mu.Unlock()
		return err
	}
	return nil
}

// admitModelAttempt atomically admits one physical provider HTTP attempt.
// Keeping this separate from token reservation is important: a retry sends
// the same prompt again, but must not spend the run's context budget again.
func admitModelAttempt(ctx context.Context) error {
	return admitModelAttemptWithContext(ctx, 1)
}

func admitModelAttemptWithContext(ctx context.Context, contextTokens int64) error {
	if contextTokens < 1 {
		contextTokens = 1
	}
	budget := runBudgetFromContext(ctx)
	if budget == nil {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if metrics := runMetricsFromContext(ctx); metrics != nil {
			metrics.recordModelRequest(contextTokens)
		}
		return nil
	}
	if err := budget.contextError(ctx); err != nil {
		return err
	}
	for {
		current := budget.modelAttempts.Load()
		if current >= agentRunMaxModelAttempts {
			return errAgentModelAttemptBudget
		}
		if !budget.modelAttempts.CompareAndSwap(current, current+1) {
			continue
		}
		// Do not admit an attempt that became canceled while contending for
		// the atomic slot. Returning the slot keeps the physical count honest.
		if err := budget.contextError(ctx); err != nil {
			budget.modelAttempts.Add(-1)
			return err
		}
		if budget.reserveModelAttempt != nil {
			reserved, err := budget.reserveModelAttempt(ctx)
			if err != nil {
				budget.modelAttempts.Add(-1)
				return err
			}
			if !reserved {
				budget.modelAttempts.Add(-1)
				return errAgentModelAttemptBudget
			}
		}
		if budget.metrics != nil {
			budget.metrics.recordModelRequest(contextTokens)
		}
		return nil
	}
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
	case errors.Is(err, errAgentModelAttemptBudget):
		return "model_attempt_budget"
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
		errors.Is(err, errAgentModelAttemptBudget) ||
		errors.Is(err, errAgentToolCallBudget) ||
		errors.Is(err, errAgentNonProgress)
}

func normalizeAgentRunError(ctx context.Context, budget *runBudget, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAgentRunDeadline) ||
		errors.Is(err, errAgentContextBudget) ||
		errors.Is(err, errAgentModelAttemptBudget) ||
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
