package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrMaxTurnsExceeded is the stable sentinel for an executor that consumed
// every allowed model/tool round while the model was still active.
var ErrMaxTurnsExceeded = errors.New("maximum turn limit exceeded")

// ErrBudgetExceeded is the stable sentinel for an invocation whose recorded
// provider spend reached or exceeded its configured USD ceiling.
var ErrBudgetExceeded = errors.New("maximum cost budget exceeded")

// ErrCostUnavailable is returned before a budgeted provider request when the
// executor cannot prove how that request will be priced.
var ErrCostUnavailable = errors.New("cost tracking unavailable")

// MaxTurnsExceededError retains the effective limit for structured callers.
// It unwraps to ErrMaxTurnsExceeded so callers never need to parse prose.
type MaxTurnsExceededError struct {
	Limit int
}

func (e *MaxTurnsExceededError) Error() string {
	return fmt.Sprintf("reached maximum turn limit (%d turns)", e.Limit)
}

func (e *MaxTurnsExceededError) Unwrap() error {
	return ErrMaxTurnsExceeded
}

// BudgetExceededError retains both the configured ceiling and the spend
// already incurred. A provider response can cross the ceiling because its
// exact token count is known only after the response arrives.
type BudgetExceededError struct {
	LimitUSD float64
	SpentUSD float64
}

func (e *BudgetExceededError) Error() string {
	return fmt.Sprintf(
		"maximum cost budget reached (limit $%.6f, spent $%.6f)",
		e.LimitUSD, e.SpentUSD)
}

func (e *BudgetExceededError) Unwrap() error {
	return ErrBudgetExceeded
}

// CostUnavailableError identifies the provider/model combination for which a
// hard cost ceiling could not be enforced safely.
type CostUnavailableError struct {
	Provider string
	Model    string
}

func (e *CostUnavailableError) Error() string {
	switch {
	case e.Provider != "" && e.Model != "":
		return fmt.Sprintf("cost tracking unavailable for provider %q model %q", e.Provider, e.Model)
	case e.Model != "":
		return fmt.Sprintf("cost tracking unavailable for model %q", e.Model)
	default:
		return "cost tracking unavailable for the selected provider/model"
	}
}

func (e *CostUnavailableError) Unwrap() error {
	return ErrCostUnavailable
}

type maxTurnsContextKey struct{}
type maxBudgetUSDContextKey struct{}

// InvocationBudgetLedger is shared by every retry and delegated agent that
// inherits an invocation context.
// Executor-local usage counters intentionally reset per Execute call;
// this ledger does not, so an outer retry cannot regain the full budget.
//
// Budgeted model requests are serialized through BeginRound/EndRound. Exact
// response cost is unknowable before generation, so one response may cross the
// ceiling; serialization prevents several concurrent agents from each doing so.
type InvocationBudgetLedger struct {
	mu              sync.Mutex
	limitUSD        float64
	spentUSD        float64
	costUnavailable *CostUnavailableError
	roundToken      chan struct{}
}

// Snapshot returns an atomic limit/spend pair.
func (b *InvocationBudgetLedger) Snapshot() (limitUSD, spentUSD float64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	limitUSD, spentUSD = b.limitUSD, b.spentUSD
	b.mu.Unlock()
	return limitUSD, spentUSD
}

// AddSpend records one provider round after its usage becomes known.
func (b *InvocationBudgetLedger) AddSpend(costUSD float64) (limitUSD, spentUSD float64) {
	if b == nil {
		return 0, 0
	}
	b.mu.Lock()
	if costUSD > 0 {
		b.spentUSD += costUSD
	}
	limitUSD, spentUSD = b.limitUSD, b.spentUSD
	b.mu.Unlock()
	return limitUSD, spentUSD
}

// MarkCostUnavailable latches an unpriceable provider/model. The first value
// wins so every descendant reports one deterministic terminal reason.
func (b *InvocationBudgetLedger) MarkCostUnavailable(provider, model string) error {
	if b == nil {
		return &CostUnavailableError{Provider: provider, Model: model}
	}
	b.mu.Lock()
	if b.costUnavailable == nil {
		b.costUnavailable = &CostUnavailableError{Provider: provider, Model: model}
	}
	failure := *b.costUnavailable
	b.mu.Unlock()
	return &failure
}

// TerminalError reports whether another provider round is forbidden.
func (b *InvocationBudgetLedger) TerminalError() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.costUnavailable != nil {
		failure := *b.costUnavailable
		return &failure
	}
	if b.limitUSD > 0 && b.spentUSD >= b.limitUSD {
		return &BudgetExceededError{LimitUSD: b.limitUSD, SpentUSD: b.spentUSD}
	}
	return nil
}

// BeginRound acquires the invocation's single provider-round lease. Waiting is
// context-cancellable; the terminal policy is rechecked after acquisition.
func (b *InvocationBudgetLedger) BeginRound(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case b.roundToken <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := b.TerminalError(); err != nil {
		b.EndRound()
		return err
	}
	return nil
}

// EndRound releases a lease acquired by BeginRound.
func (b *InvocationBudgetLedger) EndRound() {
	if b == nil {
		return
	}
	select {
	case <-b.roundToken:
	default:
	}
}

// ContextWithMaxTurns applies an invocation-scoped executor round policy.
// A positive value is a hard limit; zero explicitly disables the turn cap.
// Negative values leave the executor's adaptive default unchanged.
func ContextWithMaxTurns(ctx context.Context, maxTurns int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxTurns < 0 {
		return ctx
	}
	return context.WithValue(ctx, maxTurnsContextKey{}, maxTurns)
}

// MaxTurnsFromContext returns an invocation override when present. Zero is a
// meaningful value: it requests no turn cap.
func MaxTurnsFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	maxTurns, ok := ctx.Value(maxTurnsContextKey{}).(int)
	return maxTurns, ok && maxTurns >= 0
}

// ContextWithMaxBudgetUSD applies an invocation-scoped hard cost ceiling.
// Zero disables budget enforcement. Callers validate negative values before
// constructing the execution context.
func ContextWithMaxBudgetUSD(ctx context.Context, maxBudgetUSD float64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBudgetUSD <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxBudgetUSDContextKey{}, &InvocationBudgetLedger{
		limitUSD:   maxBudgetUSD,
		roundToken: make(chan struct{}, 1),
	})
}

// MaxBudgetUSDFromContext returns the positive invocation cost ceiling.
func MaxBudgetUSDFromContext(ctx context.Context) (float64, bool) {
	if ctx == nil {
		return 0, false
	}
	ledger, ok := ctx.Value(maxBudgetUSDContextKey{}).(*InvocationBudgetLedger)
	if !ok || ledger == nil {
		return 0, false
	}
	budget, _ := ledger.Snapshot()
	return budget, budget > 0
}

// InvocationBudgetLedgerFromContext returns the shared invocation ledger.
func InvocationBudgetLedgerFromContext(ctx context.Context) (*InvocationBudgetLedger, bool) {
	if ctx == nil {
		return nil, false
	}
	ledger, ok := ctx.Value(maxBudgetUSDContextKey{}).(*InvocationBudgetLedger)
	if !ok || ledger == nil {
		return nil, false
	}
	limit, _ := ledger.Snapshot()
	return ledger, limit > 0
}
