package rawlog

import "context"

type ctxKey struct{}

// WithRoundLogger attaches a RoundLogger to the context.
func WithRoundLogger(ctx context.Context, rl *RoundLogger) context.Context {
	return context.WithValue(ctx, ctxKey{}, rl)
}

// RoundLoggerFromContext retrieves the RoundLogger from the context, if any.
func RoundLoggerFromContext(ctx context.Context) *RoundLogger {
	rl, _ := ctx.Value(ctxKey{}).(*RoundLogger)
	return rl
}
