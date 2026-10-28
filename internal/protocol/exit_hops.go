package protocol

import "context"

const MaxExitHops = 4

type exitHopKey struct{}

func WithExitHop(ctx context.Context, hop int) context.Context {
	return context.WithValue(ctx, exitHopKey{}, hop)
}
func ExitHop(ctx context.Context) int { hop, _ := ctx.Value(exitHopKey{}).(int); return hop }
