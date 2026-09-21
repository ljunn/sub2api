package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	sitePerformanceWindow = time.Hour
	sitePerformanceLimit  = 200
)

// Scores are request-local: a 1K request must not overwrite a 4K priority, and
// reviewing the preview must not publish its learned priorities to production.
type SitePriorityScore struct {
	Score          float64 `json:"score"`
	Priority       int     `json:"priority"`
	Samples        int     `json:"samples"`
	Successes      int     `json:"successes"`
	SuccessRate    float64 `json:"success_rate"`
	P50Seconds     float64 `json:"p50_seconds"`
	P90Seconds     float64 `json:"p90_seconds"`
	SpeedSeconds   float64 `json:"speed_seconds"`
	SpeedReference float64 `json:"speed_reference"`
	SpeedEstimated bool    `json:"speed_estimated"`
	CostRatio      float64 `json:"cost_ratio"`
	Ready          bool    `json:"ready"`
}

type sitePerformanceKey struct {
	AccountID int64
	Model     string
	Tier      string
}

type sitePerformanceSample struct {
	At      time.Time
	Success bool
	Seconds float64
}

type sitePerformanceStore struct {
	mu      sync.Mutex
	samples map[sitePerformanceKey][]sitePerformanceSample
}

type sitePriorityViewKey struct {
	GroupID               int64
	Platform, Model, Tier string
}

type sitePriorityView struct {
	Until      time.Time
	Priorities map[int64]int
}

// Admin-only, short-lived cache avoids loading a model's peers once per row.
// Routing always calculates from the live request and does not use this cache.
type sitePriorityViewCache struct {
	mu     sync.Mutex
	values map[sitePriorityViewKey]sitePriorityView
}

func (s *UpstreamSitePricing) adminPriority(ctx context.Context, account *Account, p SiteAccountPolicy, tier string, now time.Time) (int, bool) {
	if s == nil || s.accounts == nil || p.LocalGroupID <= 0 {
		return 0, false
	}
	key := sitePriorityViewKey{p.LocalGroupID, account.Platform, p.LocalModel, tier}
	s.priorityViews.mu.Lock()
	defer s.priorityViews.mu.Unlock()
	if cached, ok := s.priorityViews.values[key]; ok && now.Before(cached.Until) {
		priority, found := cached.Priorities[account.ID]
		return priority, found
	}
	peers, err := s.accounts.ListModelAvailabilityCandidates(ctx, &p.LocalGroupID, []string{account.Platform}, false)
	if err != nil {
		return 0, false
	}
	ctx = context.WithValue(ctx, sitePricingKey{}, s)
	ctx = context.WithValue(ctx, siteRequestKey{}, SitePriceRequest{Model: p.LocalModel, Tier: tier, KongfangTier: tier, KongfangBillingTier: tier})
	priorities := map[int64]int{}
	for _, peer := range siteEffectivePriorities(ctx, peers) {
		if peer.sitePriority {
			priorities[peer.ID] = peer.Priority
		}
	}
	if s.priorityViews.values == nil {
		s.priorityViews.values = make(map[sitePriorityViewKey]sitePriorityView)
	}
	if len(s.priorityViews.values) >= 256 {
		for k, view := range s.priorityViews.values {
			if !now.Before(view.Until) {
				delete(s.priorityViews.values, k)
			}
		}
	}
	if len(s.priorityViews.values) < 256 {
		s.priorityViews.values[key] = sitePriorityView{now.Add(5 * time.Second), priorities}
	}
	priority, found := priorities[account.ID]
	return priority, found
}

func (s *sitePerformanceStore) record(key sitePerformanceKey, sample sitePerformanceSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.samples == nil {
		s.samples = make(map[sitePerformanceKey][]sitePerformanceSample)
	}
	// Bound retained identities as well as samples; inactive bindings expire.
	if len(s.samples) >= 4096 {
		for k, values := range s.samples {
			if len(values) == 0 || sample.At.Sub(values[len(values)-1].At) >= sitePerformanceWindow {
				delete(s.samples, k)
			}
		}
		if _, exists := s.samples[key]; !exists && len(s.samples) >= 4096 {
			return
		}
	}
	values := append(s.samples[key], sample)
	if len(values) > sitePerformanceLimit {
		copy(values, values[len(values)-sitePerformanceLimit:])
		values = values[:sitePerformanceLimit]
	}
	s.samples[key] = values
}

func (s *sitePerformanceStore) snapshot(key sitePerformanceKey, now time.Time) []sitePerformanceSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []sitePerformanceSample
	for _, sample := range s.samples[key] {
		if !sample.At.After(now) && now.Sub(sample.At) < sitePerformanceWindow {
			values = append(values, sample)
		}
	}
	return values
}

func sitePerformanceTier(p SiteAccountPolicy, request SitePriceRequest) string {
	if !p.Image {
		return "default"
	}
	if p.SiteKind == "kongfang" && request.KongfangTier != "" {
		return request.KongfangTier
	}
	if request.Tier != "" {
		return request.Tier
	}
	return "auto"
}

func sitePriorityForScore(score float64) int {
	utility := score / 100
	if math.IsNaN(utility) || math.IsInf(utility, 0) || utility <= 0 {
		return 25000
	}
	utility = math.Max(1e-12, math.Min(1, utility))
	return 1000 + int(math.Round(-2000*math.Log10(utility)))
}

// Cost is a dimensionless purchase/selling ratio, using the most expensive
// billed component. Dollars/image and dollars/million tokens are never added.
func sitePriorityCostRatio(p SiteAccountPolicy, tier string, now time.Time) (float64, bool) {
	ratio, found := 0.0, false
	for _, price := range p.Tiers {
		if tier != "auto" && price.Key != tier && price.Key != "default" {
			continue
		}
		if siteTierReason(p, price.Key, now) != "" {
			return 0, false
		}
		var selling map[string]float64
		for _, limit := range p.Limits {
			if limit.Key == price.Key {
				selling = limit.Selling
				break
			}
		}
		for component, cost := range price.Prices {
			retail, ok := selling[component]
			if !ok || math.IsNaN(retail) || math.IsInf(retail, 0) || retail < 0 {
				return 0, false
			}
			if retail == 0 {
				if cost != 0 {
					return 0, false
				}
			} else {
				ratio = math.Max(ratio, cost/retail)
			}
			found = true
		}
	}
	return ratio, found
}

func (s *UpstreamSitePricing) priorityScore(accountID int64, p SiteAccountPolicy, tier string, now time.Time) SitePriorityScore {
	result := SitePriorityScore{Priority: 25000, SuccessRate: .5, SpeedReference: 3, SpeedSeconds: 10, SpeedEstimated: true}
	if p.Image {
		result.SpeedReference, result.SpeedSeconds = 30, 120
	}
	if s == nil {
		return result
	}
	values := s.performance.snapshot(sitePerformanceKey{accountID, p.LocalModel, tier}, now)
	latencies := make([]float64, 0, len(values))
	for _, sample := range values {
		result.Samples++
		if sample.Success {
			result.Successes++
			if sample.Seconds >= 0 && !math.IsNaN(sample.Seconds) && !math.IsInf(sample.Seconds, 0) {
				latencies = append(latencies, sample.Seconds)
			}
		}
	}
	result.SuccessRate = float64(result.Successes+1) / float64(result.Samples+2)
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		quantile := func(q float64) float64 {
			position := q * float64(len(latencies)-1)
			lo, hi := int(math.Floor(position)), int(math.Ceil(position))
			return latencies[lo] + (latencies[hi]-latencies[lo])*(position-float64(lo))
		}
		result.P50Seconds, result.P90Seconds = quantile(.5), quantile(.9)
		result.SpeedSeconds = .6*result.P50Seconds + .4*result.P90Seconds
		result.SpeedEstimated = false
	}
	var priced bool
	result.CostRatio, priced = sitePriorityCostRatio(p, tier, now)
	if !priced {
		return result
	}
	result.Ready = true
	result.Score = 100 * result.SuccessRate * result.SuccessRate *
		result.SpeedReference / (result.SpeedReference + result.SpeedSeconds) * .5 / (.5 + result.CostRatio)
	result.Priority = sitePriorityForScore(result.Score)
	return result
}

// This projection copies accounts before changing priority. Repository and
// scheduler-cache objects retain their saved manual values and switches.
func siteEffectivePriorities(ctx context.Context, accounts []Account) []Account {
	pricing, _ := ctx.Value(sitePricingKey{}).(*UpstreamSitePricing)
	if pricing == nil {
		return accounts
	}
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	type poolKey struct {
		GroupID               int64
		Platform, Model, Tier string
	}
	type leader struct {
		index int
		score float64
	}
	leaders := map[poolKey]leader{}
	result := accounts
	copied := false
	for i := range accounts {
		account := &accounts[i]
		p, managed := account.SitePolicy()
		if !managed || !account.IsSchedulable() || p.LocalModel == "" {
			continue
		}
		if vetoed, _ := SitePriceVeto(ctx, account); vetoed {
			continue
		}
		p = pricing.apply(ctx, p, true)
		tier := sitePerformanceTier(p, request)
		score := pricing.priorityScore(account.ID, p, tier, time.Now())
		if !score.Ready {
			continue
		}
		if !copied {
			result = append([]Account(nil), accounts...)
			copied = true
		}
		result[i].Priority = score.Priority
		result[i].sitePriority = true
		key := poolKey{p.LocalGroupID, account.Platform, p.LocalModel, tier}
		best, exists := leaders[key]
		if !exists || score.Score > best.score || (score.Score == best.score && account.ID < accounts[best.index].ID) {
			leaders[key] = leader{i, score.Score}
		}
	}
	for _, best := range leaders {
		result[best.index].Priority = 200
	}
	return result
}

type siteForwardObservationKey struct{}
type siteForwardObservation struct {
	pricing *UpstreamSitePricing
	key     sitePerformanceKey
	image   bool
	started atomic.Int64
}

func beginSiteForward(ctx context.Context, account *Account) (context.Context, *siteForwardObservation) {
	pricing, _ := ctx.Value(sitePricingKey{}).(*UpstreamSitePricing)
	p, managed := account.SitePolicy()
	if pricing == nil || !managed || p.LocalModel == "" || ctx.Value(siteForwardObservationKey{}) != nil {
		return ctx, nil
	}
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	observation := &siteForwardObservation{pricing: pricing, key: sitePerformanceKey{account.ID, p.LocalModel, sitePerformanceTier(p, request)}, image: p.Image}
	return context.WithValue(ctx, siteForwardObservationKey{}, observation), observation
}

func markSiteForwardStarted(ctx context.Context) {
	if observation, ok := ctx.Value(siteForwardObservationKey{}).(*siteForwardObservation); ok {
		observation.started.CompareAndSwap(0, time.Now().UnixNano())
	}
}

func (s *siteForwardObservation) finish(ctx context.Context, c *gin.Context, success, disconnected bool, firstToken *int, err error) {
	if s == nil || s.started.Load() == 0 || disconnected || errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		var imageError *OpenAIImagesUpstreamError
		if errors.As(err, &imageError) && (imageError.Param != "" || openAIContentPolicyCode(openAIImagesUpstreamErrorResponseBody(imageError)) != "") {
			return
		}
		var failover *UpstreamFailoverError
		accountFailure := errors.As(err, &failover) && failover.ShouldRetryNextAccount()
		// Invalid client input and explicit refusals are not account failures.
		if !accountFailure && c != nil && c.Writer != nil {
			status := c.Writer.Status()
			if status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusUnauthorized {
				return
			}
		}
		success = false
	}
	now := time.Now()
	seconds := now.Sub(time.Unix(0, s.started.Load())).Seconds()
	if !s.image && firstToken != nil && *firstToken >= 0 {
		seconds = float64(*firstToken) / 1000
	}
	s.pricing.performance.record(s.key, sitePerformanceSample{At: now, Success: success, Seconds: seconds})
}

func (s *siteForwardObservation) finishOpenAI(ctx context.Context, c *gin.Context, result *OpenAIForwardResult, err error) {
	if s == nil {
		return
	}
	success, disconnected := result != nil && err == nil && result.SucceededForScheduling(), false
	var firstToken *int
	if result != nil {
		firstToken, disconnected = result.FirstTokenMs, result.ClientDisconnect
		if s.image && result.ImageCount <= 0 {
			success = false
		}
	}
	s.finish(ctx, c, success, disconnected, firstToken, err)
}

func (s *siteForwardObservation) finishGateway(ctx context.Context, c *gin.Context, result *ForwardResult, err error) {
	if s == nil {
		return
	}
	success, disconnected := result != nil && err == nil, false
	var firstToken *int
	if result != nil {
		firstToken, disconnected = result.FirstTokenMs, result.ClientDisconnect
		if s.image && result.ImageCount <= 0 {
			success = false
		}
	}
	s.finish(ctx, c, success, disconnected, firstToken, err)
}
