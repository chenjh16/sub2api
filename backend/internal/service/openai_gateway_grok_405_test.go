package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShouldFailoverUpstreamError_405IsFailoverEligible(t *testing.T) {
	svc := &OpenAIGatewayService{}

	assert.True(t, svc.shouldFailoverUpstreamError(http.StatusMethodNotAllowed),
		"405 should trigger failover so sticky sessions can escape to healthy accounts")
}

func TestShouldFailoverUpstreamError_ExistingCodesStillWorkThroughPolicy(t *testing.T) {
	svc := &OpenAIGatewayService{}

	systemFailoverCodes := []int{401, 402, 403, 405, 429, 529}
	for _, code := range systemFailoverCodes {
		assert.True(t, svc.shouldFailoverUpstreamError(code), "status %d should trigger failover", code)
	}

	// 5xx failover is intentionally owned by the editable gateway policy on
	// spec. The default policy preserves upstream behavior without making the
	// low-level status helper bypass an administrator disabling that rule.
	for _, code := range []int{500, 502, 503, 504} {
		assert.False(t, svc.shouldFailoverUpstreamError(code), "status %d should remain policy-controlled", code)
		assert.True(t, svc.shouldFailoverOpenAIUpstreamResponse(code, "temporary upstream failure", []byte(`{"error":{"message":"temporary upstream failure"}}`)),
			"default gateway policy should fail over status %d", code)
	}

	nonFailoverCodes := []int{200, 201, 400, 404, 408, 422}
	for _, code := range nonFailoverCodes {
		assert.False(t, svc.shouldFailoverUpstreamError(code), "status %d should NOT trigger failover", code)
	}
}
