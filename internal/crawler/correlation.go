package crawler

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type ctxKey struct{}

// WithCorrelationID attaches an ID that is sent to the crawler as gRPC metadata.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// CorrelationID returns the ID set by WithCorrelationID, or "".
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

func correlationInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if id := CorrelationID(ctx); id != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-correlation-id", id)
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}
