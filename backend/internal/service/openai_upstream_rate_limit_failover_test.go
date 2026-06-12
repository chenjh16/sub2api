package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsOpenAIUpstreamRateLimitExceededFailoverError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{
			name:       "top level rpm rate limit exceeded",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"code":"rate_limit_exceeded","message":"busy","type":"invalid_request_error"},"code":"rate_limit_exceeded","limit_type":"rpm","message":"busy"}`),
			want:       true,
		},
		{
			name:       "nested rpm limit type",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"code":"rate_limit_exceeded","limit_type":"rpm","message":"busy"}}`),
			want:       true,
		},
		{
			name:       "rate limit exceeded without rpm is not enough",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"code":"rate_limit_exceeded","message":"busy"},"code":"rate_limit_exceeded"}`),
			want:       false,
		},
		{
			name:       "rpm without rate limit exceeded code is not enough",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"busy"},"limit_type":"rpm"}`),
			want:       false,
		},
		{
			name:       "plain busy message is not enough",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"server busy, rest ten minutes"},"message":"server busy, rest ten minutes"}`),
			want:       false,
		},
		{
			name:       "only applies to 400",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"error":{"code":"rate_limit_exceeded"},"code":"rate_limit_exceeded","limit_type":"rpm"}`),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIUpstreamRateLimitExceededFailoverError(tt.statusCode, tt.body))
		})
	}
}

func TestOpenAIUpstreamRateLimitExceededRPM_ShouldFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"code":"rate_limit_exceeded","message":"busy"},"code":"rate_limit_exceeded","limit_type":"rpm"}`)

	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusBadRequest, "", body))
}

func TestOpenAIUpstreamRateLimitExceededRPM_RuntimeBlocksForTenMinutes(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 4403, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	start := time.Now()

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"error":{"code":"rate_limit_exceeded","message":"busy"},"code":"rate_limit_exceeded","limit_type":"rpm"}`),
	)

	require.True(t, shouldDisable)
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	actualUntil, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, start.Add(openAIUpstreamCooldownFallback), actualUntil, 2*time.Second)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}
