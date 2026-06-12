package service

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
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

type openAIFailoverRuleEvent struct {
	Event               string
	StatusCode          int
	Headers             http.Header
	UpstreamMessage     string
	Body                []byte
	TransportError      string
	TransportPersistent bool
	Account             *Account
}

type openAIFailoverRuleDecision struct {
	Rule         GatewayFailoverRule
	Failover     bool
	SystemReason string
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

func (s *OpenAIGatewayService) decideOpenAIFailoverRule(ctx context.Context, event openAIFailoverRuleEvent) *openAIFailoverRuleDecision {
	settings := s.openAIGatewayFailoverPolicySettings(ctx)
	if settings == nil || len(settings.Rules) == 0 {
		return nil
	}
	for _, rule := range settings.Rules {
		if !rule.Enabled || rule.Event != event.Event {
			continue
		}
		if !matchesOpenAIFailoverRule(rule, event) {
			continue
		}
		return &openAIFailoverRuleDecision{
			Rule:     rule,
			Failover: rule.Action.Failover,
		}
	}
	return nil
}

func (s *OpenAIGatewayService) decideOpenAIUpstreamHTTPFailover(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	upstreamMsg string,
	upstreamBody []byte,
) *openAIFailoverRuleDecision {
	event := openAIFailoverRuleEvent{
		Event:           GatewayFailoverRuleEventHTTPResponse,
		StatusCode:      statusCode,
		Headers:         headers,
		UpstreamMessage: upstreamMsg,
		Body:            upstreamBody,
		Account:         account,
	}
	if decision := s.decideOpenAIFailoverRule(ctx, event); decision != nil {
		return decision
	}
	if s.shouldFailoverUpstreamError(statusCode) {
		return &openAIFailoverRuleDecision{
			Failover:     true,
			SystemReason: "system_status",
		}
	}
	if isOpenAITransientProcessingError(statusCode, upstreamMsg, upstreamBody) {
		return &openAIFailoverRuleDecision{
			Failover:     true,
			SystemReason: "transient_processing",
		}
	}
	return nil
}

func matchesOpenAIFailoverRule(rule GatewayFailoverRule, event openAIFailoverRuleEvent) bool {
	match := rule.Match
	if event.Event == GatewayFailoverRuleEventHTTPResponse {
		if !matchesGatewayFailoverStatus(match, event.StatusCode) {
			return false
		}
		if !matchesGatewayFailoverJSONConditionGroup(match.JSONConditionGroup, event.Body) {
			return false
		}
		if !matchesGatewayFailoverHeaderConditionGroup(match.HeaderConditionGroup, event.Headers) {
			return false
		}
		if !matchesGatewayFailoverValueConditionGroup(match.MessageConditionGroup, event.UpstreamMessage) {
			return false
		}
		if !matchesGatewayFailoverValueConditionGroup(match.BodyConditionGroup, string(event.Body)) {
			return false
		}
		return true
	}

	if match.TransportPersistent != nil && *match.TransportPersistent != event.TransportPersistent {
		return false
	}
	if !matchesGatewayFailoverValueConditionGroup(match.TransportConditionGroup, event.TransportError) {
		return false
	}
	return true
}

func matchesGatewayFailoverStatus(match GatewayFailoverRuleMatch, statusCode int) bool {
	for _, code := range match.ExcludeStatusCodes {
		if statusCode == code {
			return false
		}
	}
	if len(match.StatusCodes) == 0 && len(match.StatusRanges) == 0 {
		return true
	}
	for _, code := range match.StatusCodes {
		if statusCode == code {
			return true
		}
	}
	for _, r := range match.StatusRanges {
		if statusCode >= r.Min && statusCode <= r.Max {
			return true
		}
	}
	return false
}

func matchesGatewayFailoverJSONConditionGroup(group *GatewayFailoverJSONConditionGroup, body []byte) bool {
	if group == nil {
		return true
	}
	total := len(group.Conditions) + len(group.Groups)
	if total == 0 {
		return true
	}
	anyMode := normalizeGatewayFailoverRuleLogic(group.Logic) == GatewayFailoverRuleLogicAny
	for _, condition := range group.Conditions {
		ok := matchesGatewayFailoverJSONCondition(condition, body)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	for i := range group.Groups {
		ok := matchesGatewayFailoverJSONConditionGroup(&group.Groups[i], body)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	return !anyMode
}

func matchesGatewayFailoverJSONCondition(condition GatewayFailoverJSONCondition, body []byte) bool {
	paths := append([]string(nil), condition.Paths...)
	if strings.TrimSpace(condition.Path) != "" {
		paths = append([]string{condition.Path}, paths...)
	}
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		result := gjson.GetBytes(body, path)
		if matchesGatewayFailoverConditionValue(result.Exists(), result.String(), GatewayFailoverValueCondition{
			Op:     condition.Op,
			Value:  condition.Value,
			Values: condition.Values,
		}) {
			return true
		}
	}
	return false
}

func matchesGatewayFailoverHeaderConditionGroup(group *GatewayFailoverHeaderConditionGroup, headers http.Header) bool {
	if group == nil {
		return true
	}
	total := len(group.Conditions) + len(group.Groups)
	if total == 0 {
		return true
	}
	anyMode := normalizeGatewayFailoverRuleLogic(group.Logic) == GatewayFailoverRuleLogicAny
	for _, condition := range group.Conditions {
		ok := matchesGatewayFailoverHeaderCondition(condition, headers)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	for i := range group.Groups {
		ok := matchesGatewayFailoverHeaderConditionGroup(&group.Groups[i], headers)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	return !anyMode
}

func matchesGatewayFailoverHeaderCondition(condition GatewayFailoverHeaderCondition, headers http.Header) bool {
	value := ""
	exists := false
	if headers != nil {
		values, ok := headers[http.CanonicalHeaderKey(condition.Name)]
		exists = ok && len(values) > 0
		value = strings.Join(values, ",")
	}
	return matchesGatewayFailoverConditionValue(exists, value, GatewayFailoverValueCondition{
		Op:     condition.Op,
		Value:  condition.Value,
		Values: condition.Values,
	})
}

func matchesGatewayFailoverValueConditionGroup(group *GatewayFailoverValueConditionGroup, text string) bool {
	if group == nil {
		return true
	}
	total := len(group.Conditions) + len(group.Groups)
	if total == 0 {
		return true
	}
	anyMode := normalizeGatewayFailoverRuleLogic(group.Logic) == GatewayFailoverRuleLogicAny
	exists := text != ""
	for _, condition := range group.Conditions {
		ok := matchesGatewayFailoverConditionValue(exists, text, condition)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	for i := range group.Groups {
		ok := matchesGatewayFailoverValueConditionGroup(&group.Groups[i], text)
		if anyMode && ok {
			return true
		}
		if !anyMode && !ok {
			return false
		}
	}
	return !anyMode
}

func matchesGatewayFailoverConditionValue(exists bool, raw string, condition GatewayFailoverValueCondition) bool {
	op := normalizeGatewayFailoverRuleOp(condition.Op)
	switch op {
	case GatewayFailoverRuleOpExists:
		return exists
	case GatewayFailoverRuleOpNotExists:
		return !exists
	}
	if !exists {
		return false
	}

	value := strings.ToLower(strings.TrimSpace(raw))
	want := strings.ToLower(strings.TrimSpace(condition.Value))
	values := make([]string, 0, len(condition.Values))
	for _, rawValue := range condition.Values {
		v := strings.ToLower(strings.TrimSpace(rawValue))
		if v != "" {
			values = append(values, v)
		}
	}
	switch op {
	case GatewayFailoverRuleOpNotEquals:
		return value != want
	case GatewayFailoverRuleOpContains:
		return want != "" && strings.Contains(value, want)
	case GatewayFailoverRuleOpNotContains:
		return want == "" || !strings.Contains(value, want)
	case GatewayFailoverRuleOpIn:
		for _, v := range values {
			if value == v {
				return true
			}
		}
		return false
	case GatewayFailoverRuleOpRegex:
		pattern := condition.Value
		if pattern == "" {
			return false
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false
		}
		return re.MatchString(raw)
	default:
		return value == want
	}
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
	s.openaiConsecutiveFailureCounters.Range(func(key, _ any) bool {
		k, ok := key.(openAIConsecutiveFailureKey)
		if ok && k.accountID == account.ID {
			s.openaiConsecutiveFailureCounters.Delete(key)
		}
		return true
	})
}

func (s *OpenAIGatewayService) applyOpenAIFailoverRuleSideEffects(ctx context.Context, account *Account, event openAIFailoverRuleEvent, rule GatewayFailoverRule) bool {
	if !isOpenAIAccount(account) {
		return rule.Action.Failover
	}
	action := rule.Action
	shouldCooldown := action.CooldownScope != GatewayFailoverCooldownScopeNone && action.CooldownSeconds > 0
	if shouldCooldown && rule.Match.Consecutive != nil && rule.Match.Consecutive.Enabled {
		count, reached := s.recordOpenAIConsecutiveFailure(
			account,
			"rule:"+rule.ID,
			rule.Match.Consecutive.Threshold,
			time.Duration(rule.Match.Consecutive.WindowSeconds)*time.Second,
		)
		if !reached {
			return action.Failover
		}
		s.applyOpenAIRuleCooldown(ctx, account, event, rule, count, rule.Match.Consecutive.Threshold)
		return action.Failover
	}
	if shouldCooldown {
		s.applyOpenAIRuleCooldown(ctx, account, event, rule, 1, 1)
	}
	return action.Failover
}

func (s *OpenAIGatewayService) applyOpenAIRuleCooldown(ctx context.Context, account *Account, event openAIFailoverRuleEvent, rule GatewayFailoverRule, count int, threshold int) {
	if s == nil || account == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	action := rule.Action
	reason := strings.TrimSpace(action.Reason)
	if reason == "" {
		reason = rule.ID
	}
	cooldown := openAIFailureCooldownWithJitter(time.Duration(action.CooldownSeconds)*time.Second, action.JitterPercent)
	if cooldown <= 0 {
		return
	}
	until := time.Now().Add(cooldown)
	s.BlockAccountScheduling(account, until, reason)
	if action.CooldownScope == GatewayFailoverCooldownScopeTempUnsched && s.accountRepo != nil {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAccountStateUpdateTimeout)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(bgCtx, account.ID, until, fmt.Sprintf("gateway failover rule %s: %s", rule.ID, reason))
	}
	s.logOpenAIFailoverPolicyCooldown(account, rule.ID, event.StatusCode, count, threshold, until, reason)
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
