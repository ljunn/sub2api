package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type siteTrafficStore struct {
	redis  *redis.Client
	mu     sync.Mutex
	memory map[string][]byte
}
type siteTrafficReservation struct {
	AccountID int64     `json:"account_id"`
	Reason    string    `json:"reason"`
	Until     time.Time `json:"until"`
}
type siteTrafficBucket struct {
	Minute int64         `json:"minute"`
	Counts map[int64]int `json:"counts"`
}
type siteTrafficPool struct {
	Credit      map[int64]float64                 `json:"credit"`
	Last        map[int64]time.Time               `json:"last"`
	Pending     map[string]siteTrafficReservation `json:"pending"`
	Probes      map[string]siteTrafficReservation `json:"probes"`
	ProbeCredit float64                           `json:"probe_credit"`
	Buckets     []siteTrafficBucket               `json:"buckets"`
}

func (p *siteTrafficPool) clean(now time.Time) {
	if p.Credit == nil {
		p.Credit = map[int64]float64{}
	}
	if p.Last == nil {
		p.Last = map[int64]time.Time{}
	}
	if p.Pending == nil {
		p.Pending = map[string]siteTrafficReservation{}
	}
	if p.Probes == nil {
		p.Probes = map[string]siteTrafficReservation{}
	}
	for k, v := range p.Pending {
		if !now.Before(v.Until) {
			delete(p.Pending, k)
		}
	}
	for k, v := range p.Probes {
		if !now.Before(v.Until) {
			delete(p.Probes, k)
		}
	}
	keep := p.Buckets[:0]
	for _, v := range p.Buckets {
		if v.Minute > now.Unix()/60-60 {
			keep = append(keep, v)
		}
	}
	p.Buckets = keep
	for id, at := range p.Last {
		if now.Sub(at) > 24*time.Hour {
			delete(p.Last, id)
			delete(p.Credit, id)
		}
	}
}
func (s *siteTrafficStore) update(ctx context.Context, key string, fn func(*siteTrafficPool)) error {
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	if s.redis == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			s.memory = map[string][]byte{}
		}
		var p siteTrafficPool
		if raw := s.memory[key]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
		}
		p.clean(time.Now())
		fn(&p)
		raw, err := json.Marshal(p)
		if err == nil {
			s.memory[key] = raw
		}
		return err
	}
	for i := 0; i < 12; i++ {
		err := s.redis.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Bytes()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			var p siteTrafficPool
			if len(raw) > 0 {
				if err = json.Unmarshal(raw, &p); err != nil {
					return err
				}
			}
			p.clean(time.Now())
			fn(&p)
			raw, err = json.Marshal(p)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error { pipe.Set(ctx, key, raw, 24*time.Hour); return nil })
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	return errors.New("site traffic reservation contention")
}
func (s *siteTrafficStore) read(ctx context.Context, key string) (siteTrafficPool, error) {
	var p siteTrafficPool
	var raw []byte
	var err error
	if s.redis == nil {
		s.mu.Lock()
		raw = append(raw, s.memory[key]...)
		s.mu.Unlock()
	} else {
		ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		raw, err = s.redis.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			err = nil
		}
	}
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &p)
	}
	p.clean(time.Now())
	return p, err
}
func siteTrafficPoolKey(groupID int64, platform, model, tier string) string {
	raw, _ := json.Marshal([]any{groupID, platform, model, tier})
	return fmt.Sprintf("site:traffic:v1:%x", sha256.Sum256(raw))
}

type siteTrafficCandidate struct {
	id       int64
	rate     float64
	priority int
	last     time.Time
}
type siteTrafficRequestKey struct{}
type siteTrafficRequest struct {
	mu                   sync.Mutex
	id, path, model, key string
	pricing              *UpstreamSitePricing
	planned, sent        bool
	chosen               int64
	reason               string
	candidates           []siteTrafficCandidate
	tier                 string
	groupID              int64
}

// This request-local object is installed only on gateway POSTs. Admin price and
// score projections cannot consume quota. Retries share the same dispatch flag.
func WithSiteTrafficRequest(ctx context.Context, path string) (context.Context, func()) {
	r := &siteTrafficRequest{id: uuid.NewString(), path: path}
	if i := strings.Index(path, "/models/"); i >= 0 {
		r.model = strings.SplitN(path[i+8:], ":", 2)[0]
	}
	return context.WithValue(ctx, siteTrafficRequestKey{}, r), r.close
}
func (r *siteTrafficRequest) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pricing == nil || r.key == "" {
		return
	}
	_ = r.pricing.traffic.update(context.Background(), r.key, func(p *siteTrafficPool) { delete(p.Pending, r.id); delete(p.Probes, r.id) })
}
func siteTrafficTier(request SitePriceRequest) string {
	if request.Tier != "" && request.Tier != "unknown" {
		return request.Tier
	}
	if request.KongfangBillingTier != "" && request.KongfangBillingTier != "unknown" {
		return request.KongfangBillingTier
	}
	return "auto"
}
func applySiteTraffic(ctx context.Context, accounts []Account) []Account {
	r, _ := ctx.Value(siteTrafficRequestKey{}).(*siteTrafficRequest)
	pricing, _ := ctx.Value(sitePricingKey{}).(*UpstreamSitePricing)
	if r == nil || pricing == nil {
		return accounts
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent {
		return accounts
	}
	if !r.planned {
		request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
		model := request.Model
		if model == "" {
			model = r.model
		}
		if model == "" {
			return accounts
		}
		configs, err := pricing.support.read(ctx)
		if err != nil {
			return accounts
		}
		now := time.Now()
		var candidates []siteTrafficCandidate
		var groupID int64
		var platform string
		imagePool := true
		for i := range accounts {
			a := &accounts[i]
			p, managed := a.SitePolicy()
			if !managed || !a.sitePriority || p.LocalModel != model {
				continue
			}
			// sitePriority is set only after manual, time, model price and status gates.
			if len(candidates) > 0 && (groupID != p.LocalGroupID || platform != a.Platform) {
				continue
			}
			groupID, platform = p.LocalGroupID, a.Platform
			imagePool = p.Image
			samples := pricing.performance.history(sitePerformanceKey{a.ID, p.LocalModel, sitePerformanceTier(p, request)}, now)
			rate := 0.0
			if e, ok := configs[p.BindingID]; ok && e.Config.active(now) {
				rate, _ = siteSupportHealth(samples, e.Config.Percent)
				rate /= 100
			}
			var last time.Time
			if len(samples) > 0 {
				last = samples[len(samples)-1].At
			}
			candidates = append(candidates, siteTrafficCandidate{a.ID, rate, a.Priority, last})
		}
		if len(candidates) == 0 {
			return accounts
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].priority != candidates[j].priority {
				return candidates[i].priority < candidates[j].priority
			}
			return candidates[i].id < candidates[j].id
		})
		r.tier = siteTrafficTier(request)
		if !imagePool {
			r.tier = "default"
			if request.LongXiaResolution != "" {
				r.tier = request.LongXiaResolution
			}
		}
		r.key = siteTrafficPoolKey(groupID, platform, model, r.tier)
		r.pricing, r.candidates, r.groupID, r.model = pricing, candidates, groupID, model
		err = pricing.traffic.update(ctx, r.key, func(pool *siteTrafficPool) {
			// Callback may run again after WATCH conflict; derive all choices anew.
			r.chosen, r.reason = candidates[0].id, "score"
			if len(pool.Pending) >= 2048 {
				return
			}
			pendingAccounts := map[int64]int{}
			pendingProbe := 0
			busy := map[int64]bool{}
			for _, v := range pool.Pending {
				pendingAccounts[v.AccountID]++
				if v.Reason == "probe" {
					pendingProbe++
					busy[v.AccountID] = true
				}
			}
			for _, v := range pool.Probes {
				busy[v.AccountID] = true
			}
			// A 5% budget shared by all non-leading candidates, oldest first.
			if pool.ProbeCredit+.05*float64(len(pool.Pending)+1)-float64(pendingProbe) >= 1-1e-9 {
				var oldest time.Time
				found := false
				for _, c := range candidates {
					last := pool.Last[c.id]
					if c.last.After(last) {
						last = c.last
					}
					if c.id == candidates[0].id || busy[c.id] || (!last.IsZero() && now.Sub(last) < 5*time.Minute) {
						continue
					}
					if !found || last.Before(oldest) {
						r.chosen, r.reason, oldest, found = c.id, "probe", last, true
					}
				}
			}
			if r.reason == "score" {
				best := 0.0
				for _, c := range candidates {
					credit := pool.Credit[c.id] + c.rate*float64(len(pool.Pending)+1) - float64(pendingAccounts[c.id])
					if c.rate > 0 && credit >= 1-1e-9 && credit > best {
						best = credit
						r.chosen, r.reason = c.id, "support"
					}
				}
			}
			pool.Pending[r.id] = siteTrafficReservation{r.chosen, r.reason, now.Add(5 * time.Minute)}
		})
		if err != nil {
			r.key = ""
			r.pricing = nil
			return accounts
		}
		r.planned = true
	}
	result := append([]Account(nil), accounts...)
	for i := range result {
		if result[i].ID == r.chosen && result[i].sitePriority {
			result[i].Priority = 100
		}
	}
	return result
}
func (r *siteTrafficRequest) start(ctx context.Context, accountID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent {
		return
	}
	r.sent = true
	if r.pricing == nil || r.key == "" {
		return
	}
	reason := r.reason
	if accountID != r.chosen {
		reason = "score"
	}
	now := time.Now()
	err := r.pricing.traffic.update(ctx, r.key, func(p *siteTrafficPool) {
		delete(p.Pending, r.id)
		eligible := map[int64]bool{}
		for _, c := range r.candidates {
			eligible[c.id] = true
			credit := math.Min(1, p.Credit[c.id]+c.rate)
			if accountID == c.id {
				credit--
			}
			if c.rate == 0 {
				credit = 0
			}
			p.Credit[c.id] = math.Max(-1, credit)
		}
		// Ineligible accounts accrue no debt, so recovery cannot release a burst.
		for id := range p.Credit {
			if !eligible[id] {
				delete(p.Credit, id)
			}
		}
		p.ProbeCredit = math.Min(1, p.ProbeCredit+.05)
		if reason == "probe" {
			p.ProbeCredit = math.Max(0, p.ProbeCredit-1)
			p.Probes[r.id] = siteTrafficReservation{accountID, reason, now.Add(30 * time.Minute)}
		}
		p.Last[accountID] = now
		minute := now.Unix() / 60
		n := len(p.Buckets)
		if n == 0 || p.Buckets[n-1].Minute != minute {
			p.Buckets = append(p.Buckets, siteTrafficBucket{minute, map[int64]int{}})
			n++
		}
		p.Buckets[n-1].Counts[accountID]++
	})
	if err != nil {
		slog.Warn("site_traffic_accounting_failed", "account_id", accountID)
		return
	}
	slog.Info("site_traffic_selection", "account_id", accountID, "group_id", r.groupID, "model", r.model, "tier", r.tier, "reason", reason)
}

type SiteTrafficStatus struct {
	Target        float64 `json:"target"`
	Effective     float64 `json:"effective"`
	Actual        float64 `json:"actual"`
	FirstAttempts int     `json:"first_attempts"`
	Total         int     `json:"total"`
	State         string  `json:"state"`
}

func (s *UpstreamSitePricing) trafficStatus(ctx context.Context, a *Account, p SiteAccountPolicy, tier string) *SiteTrafficStatus {
	configs, err := s.support.read(ctx)
	if err != nil {
		return &SiteTrafficStatus{State: "unavailable"}
	}
	e, ok := configs[p.BindingID]
	if !ok || !e.Config.Enabled {
		return nil
	}
	out := &SiteTrafficStatus{Target: e.Config.Percent}
	if !e.Config.active(time.Now()) {
		out.State = "expired"
	} else {
		out.Effective, out.State = siteSupportHealth(s.performance.history(sitePerformanceKey{a.ID, p.LocalModel, tier}, time.Now()), e.Config.Percent)
	}
	pool, err := s.traffic.read(ctx, siteTrafficPoolKey(p.LocalGroupID, a.Platform, p.LocalModel, tier))
	if err != nil {
		out.State = "unavailable"
		return out
	}
	for _, bucket := range pool.Buckets {
		for id, n := range bucket.Counts {
			out.Total += n
			if id == a.ID {
				out.FirstAttempts += n
			}
		}
	}
	if out.Total > 0 {
		out.Actual = 100 * float64(out.FirstAttempts) / float64(out.Total)
	}
	return out
}
