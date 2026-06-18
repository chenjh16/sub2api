package service

import (
	"context"
	"strings"
)

type openAIForwardSessionContextKey struct{}

type openAIForwardSessionContext struct {
	groupID     *int64
	sessionHash string
}

// WithOpenAIForwardSession attaches the active OpenAI sticky session to a forward
// attempt so failover policy actions can clear the binding when a rule requests it.
func WithOpenAIForwardSession(ctx context.Context, groupID *int64, sessionHash string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	trimmed := strings.TrimSpace(sessionHash)
	if trimmed == "" {
		return ctx
	}
	var copiedGroupID *int64
	if groupID != nil {
		v := *groupID
		copiedGroupID = &v
	}
	return context.WithValue(ctx, openAIForwardSessionContextKey{}, openAIForwardSessionContext{
		groupID:     copiedGroupID,
		sessionHash: trimmed,
	})
}

func openAIForwardSessionFromContext(ctx context.Context) (groupID *int64, sessionHash string, ok bool) {
	if ctx == nil {
		return nil, "", false
	}
	session, ok := ctx.Value(openAIForwardSessionContextKey{}).(openAIForwardSessionContext)
	if !ok || strings.TrimSpace(session.sessionHash) == "" {
		return nil, "", false
	}
	return session.groupID, strings.TrimSpace(session.sessionHash), true
}
