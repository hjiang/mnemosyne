// Package auth provides authentication, session management, and middleware.
package auth

import "context"

type contextKey struct{}

// WithUserID stores the authenticated user ID in the context.
func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, contextKey{}, userID)
}

// UserIDFromContext retrieves the authenticated user ID, or 0 if absent.
func UserIDFromContext(ctx context.Context) int64 {
	id, _ := ctx.Value(contextKey{}).(int64)
	return id
}
