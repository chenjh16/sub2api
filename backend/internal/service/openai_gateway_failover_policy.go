package service

import (
	"context"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	openAIConsecutiveFailureCategoryHTTP5xx   = "http_5xx"
	openAIConsecutiveFailureCategoryTransport = "transport"
)

type openAIConsecutiveFailureKey struct {
	accountID int64
	category  string
}

type openAIConsecutiveFailureCounter struct {
	mu          sync.Mutex
	count       int
	windowStart time.Time
}

func (s *OpenAIGatewayService) openAIGatewayFailoverPolicySettings(ctx context.Context) *GatewayFailoverPolicySettings {
	if ctx == nil {
		ctx = context.Background()
	}
	if s != nil && s.settingService != nil {
		return s.settingService.GetGatewayFailoverPolicySettingsCached(ctx)
	}
	return DefaultGatewayFailoverPolicySettings()
}

func (s *OpenAIGatewayService) isOpenAIStructured400FailoverEnabled(ctx context.Context) bool {
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	return settings == nil || settings.Structured400Enabled
}

func (s *OpenAIGatewayService) openAIStructured400CooldownDuration(ctx context.Context) time.Duration {
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	if settings == nil || settings.Structured400CooldownMinutes <= 0 {
		return openAIUpstreamCooldownFallback
	}
	return time.Duration(settings.Structured400CooldownMinutes) * time.Minute
}

func (s *OpenAIGatewayService) recordOpenAIConsecutiveFailure(account *Account, category string, threshold int, window time.Duration) (int, bool) {
	if s == nil || account == nil || account.ID <= 0 || strings.TrimSpace(category) == "" || threshold <= 0 {
		return 0, false
	}
	if window <= 0 {
		window = time.Second
	}

	key := openAIConsecutiveFailureKey{accountID: account.ID, category: category}
	raw, _ := s.openaiConsecutiveFailureCounters.LoadOrStore(key, &openAIConsecutiveFailureCounter{})
	counter, ok := raw.(*openAIConsecutiveFailureCounter)
	if !ok || counter == nil {
		counter = &openAIConsecutiveFailureCounter{}
		s.openaiConsecutiveFailureCounters.Store(key, counter)
	}

	now := time.Now()
	counter.mu.Lock()
	defer counter.mu.Unlock()

	if counter.windowStart.IsZero() || now.Sub(counter.windowStart) > window {
		counter.windowStart = now
		counter.count = 1
	} else {
		counter.count++
	}

	reached := counter.count >= threshold
	count := counter.count
	if reached {
		counter.count = 0
		counter.windowStart = time.Time{}
		s.openaiConsecutiveFailureCounters.Delete(key)
	}
	return count, reached
}

func (s *OpenAIGatewayService) clearOpenAIConsecutiveFailures(account *Account) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	s.openaiConsecutiveFailureCounters.Delete(openAIConsecutiveFailureKey{
		accountID: account.ID,
		category:  openAIConsecutiveFailureCategoryHTTP5xx,
	})
	s.openaiConsecutiveFailureCounters.Delete(openAIConsecutiveFailureKey{
		accountID: account.ID,
		category:  openAIConsecutiveFailureCategoryTransport,
	})
}

func (s *OpenAIGatewayService) maybeBlockOpenAIHTTP5xxFailure(ctx context.Context, account *Account, statusCode int) bool {
	if statusCode < http.StatusInternalServerError || statusCode == 529 || !isOpenAIAccount(account) {
		return false
	}
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	if settings == nil || !settings.HTTP5xxCooldownEnabled {
		return false
	}

	count, reached := s.recordOpenAIConsecutiveFailure(
		account,
		openAIConsecutiveFailureCategoryHTTP5xx,
		settings.HTTP5xxThreshold,
		time.Duration(settings.HTTP5xxWindowSeconds)*time.Second,
	)
	if !reached {
		return false
	}

	cooldown := openAIFailureCooldownWithJitter(
		time.Duration(settings.HTTP5xxCooldownSeconds)*time.Second,
		settings.FailureCooldownJitterPercent,
	)
	until := time.Now().Add(cooldown)
	s.BlockAccountScheduling(account, until, "http_5xx_threshold")
	s.logOpenAIFailoverPolicyCooldown(account, openAIConsecutiveFailureCategoryHTTP5xx, statusCode, count, settings.HTTP5xxThreshold, until, "http_5xx_threshold")
	return true
}

func (s *OpenAIGatewayService) maybeBlockOpenAITransportFailure(ctx context.Context, account *Account, safeErr string) bool {
	if !isOpenAIAccount(account) {
		return false
	}
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	if settings == nil || !settings.TransportCooldownEnabled {
		return false
	}

	count, reached := s.recordOpenAIConsecutiveFailure(
		account,
		openAIConsecutiveFailureCategoryTransport,
		settings.TransportThreshold,
		time.Duration(settings.TransportWindowSeconds)*time.Second,
	)
	if !reached {
		return false
	}

	cooldown := openAIFailureCooldownWithJitter(
		time.Duration(settings.TransportCooldownSeconds)*time.Second,
		settings.FailureCooldownJitterPercent,
	)
	until := time.Now().Add(cooldown)
	s.BlockAccountScheduling(account, until, "transport_threshold")
	s.logOpenAIFailoverPolicyCooldown(account, openAIConsecutiveFailureCategoryTransport, 0, count, settings.TransportThreshold, until, "transport_threshold", zap.String("transport_error", safeErr))
	return true
}

func (s *OpenAIGatewayService) logOpenAIFailoverPolicyCooldown(account *Account, category string, statusCode int, count int, threshold int, until time.Time, reason string, fields ...zap.Field) {
	if account == nil {
		return
	}
	base := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("platform", account.Platform),
		zap.String("category", category),
		zap.Int("status_code", statusCode),
		zap.Int("count", count),
		zap.Int("threshold", threshold),
		zap.Time("until", until),
		zap.String("reason", reason),
	}
	base = append(base, fields...)
	logger.L().With(zap.String("component", "service.openai_gateway")).Warn(
		"openai.account_runtime_cooldown_failover_policy",
		base...,
	)
}

func openAIFailureCooldownWithJitter(base time.Duration, jitterPercent int) time.Duration {
	if base <= 0 {
		return 0
	}
	if jitterPercent <= 0 {
		return base
	}
	if jitterPercent > 100 {
		jitterPercent = 100
	}
	maxDelta := time.Duration(int64(base) * int64(jitterPercent) / 100)
	if maxDelta <= 0 {
		return base
	}
	delta := time.Duration(rand.Int63n(int64(maxDelta)*2+1)) - maxDelta
	withJitter := base + delta
	if withJitter < time.Second {
		return time.Second
	}
	return withJitter
}
