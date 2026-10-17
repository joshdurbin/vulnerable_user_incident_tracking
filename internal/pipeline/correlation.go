package pipeline

import (
	"context"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

const correlationHeader = "x-correlation-id"

type corrKey struct{}

// CorrelationID returns the correlation ID on ctx ("" if none).
func CorrelationID(ctx context.Context) string { s, _ := ctx.Value(corrKey{}).(string); return s }

// WithCorrelationID attaches an ID to a Go context (clients, activities).
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, corrKey{}, id)
}

// WithWorkflowCorrelationID attaches an ID to a workflow context so every
// activity and child workflow started from it carries the header.
func WithWorkflowCorrelationID(ctx workflow.Context, id string) workflow.Context {
	return workflow.WithValue(ctx, corrKey{}, id)
}

// Propagator carries the correlation ID in Temporal headers across clients,
// workflows, child workflows and activities, so one ID ties together the CLI
// call, every activity log line, and the Python crawler's gRPC metadata.
type Propagator struct{}

var _ workflow.ContextPropagator = Propagator{}

func (Propagator) Inject(ctx context.Context, w workflow.HeaderWriter) error {
	return write(w, CorrelationID(ctx))
}
func (Propagator) InjectFromWorkflow(ctx workflow.Context, w workflow.HeaderWriter) error {
	s, _ := ctx.Value(corrKey{}).(string)
	return write(w, s)
}
func (Propagator) Extract(ctx context.Context, r workflow.HeaderReader) (context.Context, error) {
	if id := read(r); id != "" {
		return WithCorrelationID(ctx, id), nil
	}
	return ctx, nil
}
func (Propagator) ExtractToWorkflow(ctx workflow.Context, r workflow.HeaderReader) (workflow.Context, error) {
	if id := read(r); id != "" {
		return WithWorkflowCorrelationID(ctx, id), nil
	}
	return ctx, nil
}

func write(w workflow.HeaderWriter, id string) error {
	if id == "" {
		return nil
	}
	p, err := converter.GetDefaultDataConverter().ToPayload(id)
	if err != nil {
		return err
	}
	w.Set(correlationHeader, p)
	return nil
}

func read(r workflow.HeaderReader) string {
	p, ok := r.Get(correlationHeader)
	if !ok || p == nil {
		return ""
	}
	var id string
	if err := converter.GetDefaultDataConverter().FromPayload((*commonpb.Payload)(p), &id); err != nil {
		return ""
	}
	return id
}
