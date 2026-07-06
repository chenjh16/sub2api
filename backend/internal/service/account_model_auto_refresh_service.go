package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	accountModelAutoRefreshEnabledKey           = "model_auto_refresh_enabled"
	accountModelAutoRefreshIntervalMinutesKey   = "model_auto_refresh_interval_minutes"
	accountModelAutoRefreshTestFilterEnabledKey = "model_auto_refresh_test_filter_enabled"
	accountModelAutoRefreshLastRunAtKey         = "model_auto_refresh_last_run_at"
	accountModelAutoRefreshLastSuccessAtKey     = "model_auto_refresh_last_success_at"
	accountModelAutoRefreshLastErrorKey         = "model_auto_refresh_last_error"
	accountModelAutoRefreshLastSyncedCountKey   = "model_auto_refresh_last_synced_count"
	accountModelAutoRefreshLastEnabledCountKey  = "model_auto_refresh_last_enabled_count"
	accountModelAutoRefreshLastFailedCountKey   = "model_auto_refresh_last_failed_count"

	accountModelAutoRefreshDefaultIntervalMinutes = 24 * 60
	accountModelAutoRefreshMinIntervalMinutes     = 10
	accountModelAutoRefreshMaxIntervalMinutes     = 30 * 24 * 60
	accountModelAutoRefreshPageSize               = 250
	accountModelAutoRefreshMaxWorkers             = 4
	accountModelAutoRefreshTimeout                = 15 * time.Minute
)

type accountModelAutoRefreshConfig struct {
	Enabled         bool
	IntervalMinutes int
	TestFilter      bool
	LastRunAt       *time.Time
}

// AccountModelAutoRefreshService periodically syncs upstream model lists for
// accounts that opt in through credentials.
type AccountModelAutoRefreshService struct {
	accountRepo    AccountRepository
	accountTestSvc *AccountTestService
	settingSvc     *SettingService
	cfg            *config.Config

	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	running   sync.Map
}

func NewAccountModelAutoRefreshService(
	accountRepo AccountRepository,
	accountTestSvc *AccountTestService,
	settingSvc *SettingService,
	cfg *config.Config,
) *AccountModelAutoRefreshService {
	return &AccountModelAutoRefreshService{
		accountRepo:    accountRepo,
		accountTestSvc: accountTestSvc,
		settingSvc:     settingSvc,
		cfg:            cfg,
		stopCh:         make(chan struct{}),
	}
}

// Start begins the periodic scan. The first pass is delayed so startup work and
// migrations can settle before account credentials are updated.
func (s *AccountModelAutoRefreshService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.loop()
		logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] started (tick=every minute)")
	})
}

func (s *AccountModelAutoRefreshService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *AccountModelAutoRefreshService) loop() {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-timer.C:
			s.scanOnce()
			timer.Reset(time.Minute)
		}
	}
}

func (s *AccountModelAutoRefreshService) scanOnce() {
	if s == nil || s.accountRepo == nil || s.accountTestSvc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountModelAutoRefreshTimeout)
	defer cancel()

	now := accountModelAutoRefreshNow(s.cfg)
	accounts, err := s.listAutoRefreshAccounts(ctx)
	if err != nil {
		logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] list accounts error: %v", err)
		return
	}
	if len(accounts) == 0 {
		return
	}

	sem := make(chan struct{}, accountModelAutoRefreshMaxWorkers)
	var wg sync.WaitGroup
	for i := range accounts {
		account := accounts[i]
		cfg := accountModelAutoRefreshConfigFromAccount(&account)
		if !cfg.Enabled || !accountModelAutoRefreshDue(cfg, now) {
			continue
		}
		if _, loaded := s.running.LoadOrStore(account.ID, struct{}{}); loaded {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(acc Account, cfg accountModelAutoRefreshConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			defer s.running.Delete(acc.ID)
			s.refreshOneAccount(ctx, &acc, cfg, now)
		}(account, cfg)
	}
	wg.Wait()
}

func (s *AccountModelAutoRefreshService) listAutoRefreshAccounts(ctx context.Context) ([]Account, error) {
	var out []Account
	for page := 1; ; page++ {
		accounts, result, err := s.accountRepo.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  accountModelAutoRefreshPageSize,
			SortBy:    "id",
			SortOrder: pagination.SortOrderAsc,
		})
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if accountModelAutoRefreshConfigFromAccount(&account).Enabled {
				out = append(out, account)
			}
		}
		if len(accounts) == 0 || result == nil || page >= result.Pages {
			break
		}
	}
	return out, nil
}

func (s *AccountModelAutoRefreshService) refreshOneAccount(ctx context.Context, account *Account, cfg accountModelAutoRefreshConfig, now time.Time) {
	if account == nil {
		return
	}
	credentials := shallowCopyMap(account.Credentials)
	credentials[accountModelAutoRefreshLastRunAtKey] = now.Format(time.RFC3339)

	models, err := s.accountTestSvc.FetchUpstreamSupportedModels(ctx, account)
	if err != nil {
		s.persistAutoRefreshFailure(ctx, account, credentials, fmt.Sprintf("sync upstream models: %v", err), now)
		return
	}
	models = normalizeAccountModelAutoRefreshModels(models)
	if len(models) == 0 {
		s.persistAutoRefreshFailure(ctx, account, credentials, "upstream returned no supported models", now)
		return
	}

	if cfg.TestFilter {
		concurrency := s.modelTestConcurrency(ctx)
		passed, failed := s.testFetchedModels(ctx, account.ID, models, concurrency)
		if len(passed) == 0 {
			credentials[accountModelAutoRefreshLastSyncedCountKey] = len(models)
			credentials[accountModelAutoRefreshLastEnabledCountKey] = 0
			credentials[accountModelAutoRefreshLastFailedCountKey] = len(failed)
			s.persistAutoRefreshFailure(ctx, account, credentials, "all fetched models failed automatic tests; existing model list was preserved", now)
			return
		}
		applyAccountModelAutoRefreshFilteredModels(credentials, models, passed)
		credentials[accountModelAutoRefreshLastEnabledCountKey] = len(passed)
		credentials[accountModelAutoRefreshLastFailedCountKey] = len(failed)
	} else {
		applyAccountModelAutoRefreshFetchedModels(credentials, models)
		credentials[accountModelAutoRefreshLastEnabledCountKey] = len(parseAccountModelMappingCredential(credentials))
		credentials[accountModelAutoRefreshLastFailedCountKey] = 0
	}

	credentials[accountModelAutoRefreshLastSuccessAtKey] = now.Format(time.RFC3339)
	credentials[accountModelAutoRefreshLastSyncedCountKey] = len(models)
	delete(credentials, accountModelAutoRefreshLastErrorKey)
	if err := persistAccountCredentials(ctx, s.accountRepo, account, credentials); err != nil {
		logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] account=%d persist success error: %v", account.ID, err)
		return
	}
	logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] account=%d refreshed models=%d test_filter=%v", account.ID, len(models), cfg.TestFilter)
}

func (s *AccountModelAutoRefreshService) persistAutoRefreshFailure(ctx context.Context, account *Account, credentials map[string]any, message string, now time.Time) {
	if account == nil {
		return
	}
	credentials[accountModelAutoRefreshLastErrorKey] = strings.TrimSpace(message)
	delete(credentials, accountModelAutoRefreshLastSuccessAtKey)
	if err := persistAccountCredentials(ctx, s.accountRepo, account, credentials); err != nil {
		logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] account=%d persist failure error: %v", account.ID, err)
		return
	}
	logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] account=%d failed at=%s err=%s", account.ID, now.Format(time.RFC3339), message)
}

func (s *AccountModelAutoRefreshService) modelTestConcurrency(ctx context.Context) int {
	settings := DefaultModelMappingAutomationSettings()
	if s != nil && s.settingSvc != nil {
		if loaded, err := s.settingSvc.GetModelMappingAutomationSettings(ctx); err == nil && loaded != nil {
			settings = loaded
		}
	}
	return normalizeModelBatchTestConcurrency(settings.BatchTestConcurrency)
}

func (s *AccountModelAutoRefreshService) testFetchedModels(ctx context.Context, accountID int64, models []string, concurrency int) ([]string, []string) {
	if s == nil || s.accountTestSvc == nil {
		return nil, models
	}
	if concurrency <= 0 {
		concurrency = modelBatchTestDefaultConcurrency
	}
	concurrency = normalizeModelBatchTestConcurrency(concurrency)

	type result struct {
		model   string
		passed  bool
		message string
	}
	results := make(chan result, len(models))
	queue := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for model := range queue {
				select {
				case <-ctx.Done():
					results <- result{model: model, passed: false, message: ctx.Err().Error()}
					continue
				default:
				}
				testResult, err := s.accountTestSvc.RunTestBackground(ctx, accountID, model)
				passed := err == nil && testResult != nil && testResult.Status == "success"
				message := ""
				if err != nil {
					message = err.Error()
				} else if testResult != nil {
					message = testResult.ErrorMessage
				}
				results <- result{model: model, passed: passed, message: message}
			}
		}()
	}

	go func() {
		defer close(queue)
		for _, model := range models {
			select {
			case <-ctx.Done():
				return
			case queue <- model:
			}
		}
	}()

	wg.Wait()
	close(results)

	passedSet := make(map[string]struct{}, len(models))
	failedSet := make(map[string]struct{}, len(models))
	for res := range results {
		if res.passed {
			passedSet[res.model] = struct{}{}
		} else {
			failedSet[res.model] = struct{}{}
			if strings.TrimSpace(res.message) != "" {
				logger.LegacyPrintf("service.account_model_auto_refresh", "[AccountModelAutoRefresh] account=%d model=%s test failed: %s", accountID, res.model, res.message)
			}
		}
	}

	passed := make([]string, 0, len(passedSet))
	failed := make([]string, 0, len(failedSet))
	for _, model := range models {
		if _, ok := passedSet[model]; ok {
			passed = append(passed, model)
		}
		if _, ok := failedSet[model]; ok {
			failed = append(failed, model)
		}
	}
	return passed, failed
}

func accountModelAutoRefreshNow(cfg *config.Config) time.Time {
	loc := time.Local
	if cfg != nil && strings.TrimSpace(cfg.Timezone) != "" {
		if parsed, err := time.LoadLocation(cfg.Timezone); err == nil && parsed != nil {
			loc = parsed
		}
	}
	return time.Now().In(loc)
}

func accountModelAutoRefreshConfigFromAccount(account *Account) accountModelAutoRefreshConfig {
	cfg := accountModelAutoRefreshConfig{
		IntervalMinutes: accountModelAutoRefreshDefaultIntervalMinutes,
	}
	if account == nil || account.Credentials == nil {
		return cfg
	}
	cfg.Enabled = credentialBool(account.Credentials[accountModelAutoRefreshEnabledKey])
	cfg.TestFilter = credentialBool(account.Credentials[accountModelAutoRefreshTestFilterEnabledKey])
	cfg.IntervalMinutes = normalizeAccountModelAutoRefreshIntervalMinutes(account.Credentials[accountModelAutoRefreshIntervalMinutesKey])
	cfg.LastRunAt = account.GetCredentialAsTime(accountModelAutoRefreshLastRunAtKey)
	return cfg
}

func normalizeAccountModelAutoRefreshIntervalMinutes(value any) int {
	n := accountModelAutoRefreshDefaultIntervalMinutes
	switch v := value.(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			n = int(parsed)
		}
	case string:
		if parsed, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			n = int(parsed.Minutes())
		} else if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			n = parsed
		}
	}
	if n < accountModelAutoRefreshMinIntervalMinutes {
		return accountModelAutoRefreshMinIntervalMinutes
	}
	if n > accountModelAutoRefreshMaxIntervalMinutes {
		return accountModelAutoRefreshMaxIntervalMinutes
	}
	return n
}

func accountModelAutoRefreshDue(cfg accountModelAutoRefreshConfig, now time.Time) bool {
	if !cfg.Enabled {
		return false
	}
	if cfg.LastRunAt == nil || cfg.LastRunAt.IsZero() {
		return true
	}
	return !now.Before(cfg.LastRunAt.Add(time.Duration(cfg.IntervalMinutes) * time.Minute))
}

func normalizeAccountModelAutoRefreshModels(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func applyAccountModelAutoRefreshFetchedModels(credentials map[string]any, fetched []string) {
	if credentials == nil {
		return
	}
	candidates := mergeCredentialModelList(parseAccountModelListCredential(credentials["model_candidates"]), fetched)
	if len(candidates) > 0 {
		credentials["model_candidates"] = candidates
	}
}

func applyAccountModelAutoRefreshFilteredModels(credentials map[string]any, fetched []string, passed []string) {
	if credentials == nil || len(passed) == 0 {
		return
	}

	fetchedSet := lowerModelSet(fetched)
	passedSet := lowerModelSet(passed)

	currentCandidates := parseAccountModelListCredential(credentials["model_candidates"])
	nextCandidates := make([]string, 0, len(currentCandidates)+len(passed))
	for _, model := range currentCandidates {
		lower := strings.ToLower(model)
		if _, fetched := fetchedSet[lower]; fetched {
			if _, ok := passedSet[lower]; !ok {
				continue
			}
		}
		nextCandidates = append(nextCandidates, model)
	}
	nextCandidates = mergeCredentialModelList(nextCandidates, passed)
	credentials["model_candidates"] = nextCandidates
	credentials["model_selection_enabled"] = true

	mapping := parseAccountModelMappingCredential(credentials)
	nextMapping := make(map[string]any, len(mapping)+len(passed))
	for from, to := range mapping {
		fromLower := strings.ToLower(from)
		toLower := strings.ToLower(to)
		if _, fetched := fetchedSet[fromLower]; fetched {
			if _, ok := passedSet[fromLower]; !ok {
				continue
			}
		}
		if _, fetched := fetchedSet[toLower]; fetched {
			if _, ok := passedSet[toLower]; !ok {
				continue
			}
		}
		nextMapping[from] = to
	}
	for _, model := range passed {
		if _, ok := nextMapping[model]; !ok {
			nextMapping[model] = model
		}
	}
	if len(nextMapping) > 0 {
		credentials["model_mapping"] = nextMapping
	} else {
		delete(credentials, "model_mapping")
	}
}

func parseAccountModelListCredential(value any) []string {
	switch v := value.(type) {
	case []string:
		return normalizeAccountModelAutoRefreshModels(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return normalizeAccountModelAutoRefreshModels(out)
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		var parsed []string
		if err := json.Unmarshal([]byte(v), &parsed); err == nil {
			return normalizeAccountModelAutoRefreshModels(parsed)
		}
		return normalizeAccountModelAutoRefreshModels(strings.Split(v, ","))
	default:
		return nil
	}
}

func parseAccountModelMappingCredential(credentials map[string]any) map[string]string {
	if credentials == nil {
		return nil
	}
	switch raw := credentials["model_mapping"].(type) {
	case map[string]string:
		out := make(map[string]string, len(raw))
		for key, value := range raw {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if key != "" && value != "" {
				out[key] = value
			}
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(raw))
		for key, value := range raw {
			str, ok := value.(string)
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			str = strings.TrimSpace(str)
			if key != "" && str != "" {
				out[key] = str
			}
		}
		return out
	default:
		return nil
	}
}

func mergeCredentialModelList(existing []string, additions []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additions))
	out := make([]string, 0, len(existing)+len(additions))
	for _, list := range [][]string{existing, additions} {
		for _, model := range list {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			key := strings.ToLower(model)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model)
		}
	}
	sort.Strings(out)
	return out
}

func lowerModelSet(models []string) map[string]struct{} {
	out := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		out[strings.ToLower(model)] = struct{}{}
	}
	return out
}
