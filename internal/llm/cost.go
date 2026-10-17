package llm

import (
	"errors"
	"fmt"
	"sync"
)

// Price is USD per million tokens.
type Price struct {
	InputPerM  float64 `json:"input_per_m" mapstructure:"input_per_m"`
	OutputPerM float64 `json:"output_per_m" mapstructure:"output_per_m"`
}

// Cost computes the estimated USD cost of a call. Unknown models cost 0 and
// are reported via the second return value so callers can warn.
func Cost(prices map[string]Price, model string, in, out int64) (float64, bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	return float64(in)/1e6*p.InputPerM + float64(out)/1e6*p.OutputPerM, true
}

// ErrBudgetExceeded is returned when the spending limit has been reached.
var ErrBudgetExceeded = errors.New("llm budget exceeded")

// Budget gates LLM spend. Implementations must be safe for concurrent use.
type Budget interface {
	// Check returns ErrBudgetExceeded if no more spend is allowed.
	Check() error
	// Record adds actual spend.
	Record(costUSD float64)
}

// MemoryBudget is a process-local monthly-limit budget (a persistent one backed
// by Postgres replaces it in the worker). A limit <= 0 means unlimited.
type MemoryBudget struct {
	mu    sync.Mutex
	limit float64
	spent float64
}

func NewMemoryBudget(limitUSD float64) *MemoryBudget { return &MemoryBudget{limit: limitUSD} }

func (b *MemoryBudget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && b.spent >= b.limit {
		return fmt.Errorf("%w: spent $%.4f of $%.2f", ErrBudgetExceeded, b.spent, b.limit)
	}
	return nil
}

func (b *MemoryBudget) Record(c float64) {
	b.mu.Lock()
	b.spent += c
	b.mu.Unlock()
}

func (b *MemoryBudget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}
