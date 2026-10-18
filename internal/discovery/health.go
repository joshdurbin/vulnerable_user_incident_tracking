package discovery

import (
	"fmt"
	"time"
)

// RunStatus is the outcome of one discovery run.
type RunStatus string

const (
	RunSuccess RunStatus = "success"
	RunPartial RunStatus = "partial" // the provider failed part way but returned usable candidates
	RunFailed  RunStatus = "failed"
)

// RunRecord is one persisted discovery run, kept so a source's health can be seen and trended.
type RunRecord struct {
	Slug       string
	StartedAt  time.Time
	FinishedAt time.Time
	Window     Window
	Status     RunStatus
	Found      int
	Unique     int
	New        int
	Known      int
	Pending    int
	Unresolved int
	Requests   int
	Truncated  bool
	Warnings   []string
	Error      string
}

func (r RunRecord) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// RunStats summarises a source's runs over a window.
type RunStats struct {
	Runs, Successes, Partials, Failures int64
	AvgSeconds, P95Seconds              float64
	NewURLs, Requests                   int64
}

// SuccessRate is the share of runs that were not failures; 0 when there were none.
func (s RunStats) SuccessRate() float64 {
	if s.Runs == 0 {
		return 0
	}
	return float64(s.Runs-s.Failures) / float64(s.Runs)
}

// HealthState is a source's overall condition, in order of how much attention it needs.
type HealthState string

const (
	HealthDisabled HealthState = "disabled"
	HealthPaused   HealthState = "paused"  // enabled, but its schedule is paused
	HealthWaiting  HealthState = "waiting" // enabled, no run yet
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded" // the latest run(s) failed, but not repeatedly
	HealthStale    HealthState = "stale"    // runs are not succeeding often enough
	HealthFailing  HealthState = "failing"  // repeated failures in a row
)

// FailingAfter is how many consecutive failed runs make a source "failing".
const FailingAfter = 3

// Health is a source's condition with a short human reason.
type Health struct {
	State  HealthState `json:"state"`
	Reason string      `json:"reason"`
}

// AssessHealth derives a source's health from its stored state. schedulePaused is whether
// its Temporal Schedule is paused (unknown is false). A source is stale when it has not
// succeeded within three poll intervals (and at least an hour), which catches a schedule
// that silently stopped as well as a source that keeps timing out.
func AssessHealth(s Source, schedulePaused bool, now time.Time) Health {
	switch {
	case !s.Enabled:
		return Health{HealthDisabled, "source is disabled"}
	case schedulePaused:
		return Health{HealthPaused, "schedule is paused"}
	case s.LastAttemptAt == nil:
		return Health{HealthWaiting, "no run yet"}
	case s.ConsecutiveFailures >= FailingAfter:
		return Health{HealthFailing, fmt.Sprintf("%d runs in a row failed: %s", s.ConsecutiveFailures, s.ErrorState)}
	}
	limit := 3 * s.PollInterval
	if limit < time.Hour {
		limit = time.Hour
	}
	switch {
	case s.LastSuccessAt == nil:
		return Health{HealthStale, "has never succeeded"}
	case now.Sub(*s.LastSuccessAt) > limit:
		return Health{HealthStale, fmt.Sprintf("last success %s ago", humanDuration(now.Sub(*s.LastSuccessAt)))}
	case s.ConsecutiveFailures > 0:
		return Health{HealthDegraded, fmt.Sprintf("last run failed: %s", s.ErrorState)}
	}
	return Health{HealthHealthy, "running normally"}
}

// humanDuration writes a duration the way a person would say it: "40 min", "5 h", "2 d 3 h".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	case d < 48*time.Hour:
		h := int(d.Round(time.Hour) / time.Hour)
		return fmt.Sprintf("%d h", h)
	}
	days := int(d / (24 * time.Hour))
	if h := int((d % (24 * time.Hour)) / time.Hour); h > 0 {
		return fmt.Sprintf("%d d %d h", days, h)
	}
	return fmt.Sprintf("%d d", days)
}
