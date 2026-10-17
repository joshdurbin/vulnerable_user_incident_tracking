package pipeline

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var defaultFollowUps = []time.Duration{48 * time.Hour, 7 * 24 * time.Hour}

const (
	defaultIdleTimeout = 30 * 24 * time.Hour
	maxFollowUpURLs    = 10
)

// IncidentWorkflow is the long-lived companion of one incident. It:
//   - receives article-linked Signals (SignalWithStart creates it on the first one),
//   - waits on durable timers (48h, 7d by default) and then searches for follow-up
//     coverage, because an injury often becomes a fatality days later,
//   - holds possible-duplicate reviews open and applies a human decision delivered
//     as a validated Update,
//   - answers a status Query, and Continue-As-New keeps its history bounded.
//
// It is not what keeps resolution race-free (an advisory lock in PostgreSQL
// does that); it is what makes an incident's lifecycle visible and durable.
func IncidentWorkflow(ctx workflow.Context, in IncidentInput) error {
	delays := in.FollowUpDelays
	if len(delays) == 0 {
		delays = defaultFollowUps
	}
	idle := in.IdleTimeout
	if idle == 0 {
		idle = defaultIdleTimeout
	}
	if in.FirstLinkedAt.IsZero() {
		in.FirstLinkedAt = workflow.Now(ctx)
	}
	upsertAttrs(ctx, in.Options.SearchAttributes, kw(SAIncidentID, in.IncidentID), kw(SAWorkflowKind, "incident"))

	st := IncidentStatus{IncidentID: in.IncidentID, ArticlesLinked: in.ArticlesLinked, FollowUpsDone: in.FollowUpsDone, Phase: "waiting"}
	pending := map[uuid.UUID]bool{}
	merged, ready := false, false               // ready: open reviews loaded; an update-with-start can arrive before that
	wake := workflow.NewBufferedChannel(ctx, 1) // lets a handler interrupt the main loop's wait
	mu := workflow.NewMutex(ctx)

	setNext := func() {
		if st.FollowUpsDone < len(delays) {
			st.NextFollowUp = in.FirstLinkedAt.Add(delays[st.FollowUpsDone]).Format(time.RFC3339)
		} else {
			st.NextFollowUp = ""
		}
	}
	setNext()

	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (IncidentStatus, error) {
		s := st
		s.PendingReviews = len(pending)
		return s, nil
	}); err != nil {
		return err
	}

	// Human decisions arrive as Updates. The validator rejects bad requests
	// before they are written to history.
	if err := workflow.SetUpdateHandlerWithOptions(ctx, UpdateDecideReview,
		func(ctx workflow.Context, d ReviewDecision) (*ReviewOutcome, error) {
			if err := workflow.Await(ctx, func() bool { return ready }); err != nil {
				return nil, err
			}
			if err := mu.Lock(ctx); err != nil {
				return nil, err
			}
			defer mu.Unlock()
			if !pending[d.ReviewID] {
				return nil, temporal.NewNonRetryableApplicationError("review is no longer pending", ErrTypeNotPending, nil)
			}
			var out ReviewOutcome
			a := act.RejectReview
			if d.Approve {
				a = act.ApproveReview
			}
			if err := workflow.ExecuteActivity(dbCtx(ctx), a, d).Get(ctx, &out); err != nil {
				return nil, err
			}
			delete(pending, d.ReviewID)
			if out.Approved {
				merged = true // this incident was folded into the target; nothing left to follow
				wake.SendAsync(true)
			}
			return &out, nil
		},
		workflow.UpdateHandlerOptions{Validator: func(ctx workflow.Context, d ReviewDecision) error {
			if d.ReviewID == uuid.Nil {
				return errors.New("review_id is required")
			}
			if d.Actor == "" {
				return errors.New("actor is required")
			}
			// Membership in the pending set is checked in the handler, after the set is loaded.
			return nil
		}},
	); err != nil {
		return err
	}

	sig := workflow.GetSignalChannel(ctx, SignalArticleLinked)
	loadPending := func() {
		var ids []uuid.UUID
		if err := workflow.ExecuteActivity(dbCtx(ctx), act.PendingReviews, in.IncidentID).Get(ctx, &ids); err == nil {
			clear(pending)
			for _, id := range ids {
				pending[id] = true
			}
		}
	}
	loadPending() // a Continue-As-New run starts with reviews already open
	ready = true

	for !merged {
		if workflow.GetInfo(ctx).GetContinueAsNewSuggested() {
			var m ArticleLinked
			for sig.ReceiveAsync(&m) { // drain: signals must not be lost across the restart
				st.ArticlesLinked++
			}
			_ = workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) })
			in.ArticlesLinked, in.FollowUpsDone = st.ArticlesLinked, st.FollowUpsDone
			return workflow.NewContinueAsNewError(ctx, IncidentWorkflow, in)
		}

		var wait time.Duration
		followUpDue := st.FollowUpsDone < len(delays)
		if followUpDue {
			wait = in.FirstLinkedAt.Add(delays[st.FollowUpsDone]).Sub(workflow.Now(ctx))
			if wait < 0 {
				wait = 0
			}
			st.Phase = "waiting"
		} else {
			wait = idle
			st.Phase = "idle"
		}

		timerCtx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(timerCtx, wait)
		fired, gotSignal, outcome := false, false, ""
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(sig, func(c workflow.ReceiveChannel, _ bool) {
			var m ArticleLinked
			c.Receive(ctx, &m)
			st.ArticlesLinked++
			gotSignal, outcome = true, m.Outcome
		})
		sel.AddReceive(wake, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil) })
		sel.AddFuture(timer, func(f workflow.Future) { fired = f.Get(ctx, nil) == nil })
		sel.Select(ctx)
		cancel()

		switch {
		case gotSignal:
			if outcome == "review" {
				loadPending()
			}
			// New coverage can change the canonical record; wait for the next signal/timer.
		case fired && followUpDue:
			st.Phase = "following_up"
			runFollowUp(ctx, in, st.FollowUpsDone+1)
			st.FollowUpsDone++
			setNext()
		case fired:
			// Idle past the retirement window with no new coverage: done.
			return nil
		}
	}
	return workflow.Await(ctx, func() bool { return workflow.AllHandlersFinished(ctx) })
}

// runFollowUp searches for later coverage and processes whatever it finds.
// A failure here is recorded but never fails the incident's workflow.
func runFollowUp(ctx workflow.Context, in IncidentInput, attempt int) {
	var out FollowUpOutput
	err := workflow.ExecuteActivity(discoveryCtx(ctx), act.FollowUp, FollowUpInput{IncidentID: in.IncidentID, Attempt: attempt}).Get(ctx, &out)
	if err != nil {
		workflow.GetLogger(ctx).Warn("follow-up search failed", "incident", in.IncidentID, "attempt", attempt, "error", err)
		return
	}
	refs := out.ToProcess
	if len(refs) > maxFollowUpURLs {
		refs = refs[:maxFollowUpURLs]
	}
	for _, ref := range refs {
		if _, err := startArticle(ctx, ref, "follow-up", workflow.GetInfo(ctx).WorkflowExecution.ID, in.Options); err != nil {
			workflow.GetLogger(ctx).Warn("start follow-up article failed", "url", ref.URL, "error", err)
		}
	}
}
