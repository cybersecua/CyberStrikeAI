// Package ctxkeys defines shared context key types used across packages to
// avoid import cycles. Packages that need to set or read the same context
// value import this package rather than each other.
package ctxkeys

import "context"

// ConversationIDKey is the context key for a conversation ID value.
// Both the agent package (tool dispatch) and the mcp package (HTTP request
// handling) use the same key type so that a value written by one is visible
// to the other without a direct package dependency.
type ConversationIDKey struct{}

// WithConversationID returns a context carrying id. If id is empty the
// original context is returned unchanged.
func WithConversationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, ConversationIDKey{}, id)
}

// ConversationIDFromContext returns the conversation id stored in ctx by
// WithConversationID, or "" if the context carries none.
func ConversationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(ConversationIDKey{}).(string)
	return v
}
