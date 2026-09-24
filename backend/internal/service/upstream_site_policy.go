package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
)

type siteRequestKey struct{}
type SitePriceRequest struct {
	VideoResolution                 string
	VideoDuration                   int
	LongXiaVideo                    bool
	LongXiaResolution               string
	LongXiaDuration                 int64
	WuzuFields                      map[string]string
	VividAITier                     string
	VividAIDuration                 int64
	KongfangTier                    string
	KongfangBillingTier             string
	KongfangUnsupportedImageRequest bool
	Tier                            string
	Model                           string
	UnpricedServiceTier             bool
}

func WithSitePriceRequest(ctx context.Context, body []byte) context.Context {
	request := SitePriceRequest{Model: gjson.GetBytes(body, "model").String()}
	request.VideoResolution = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "resolution").String()))
	request.VideoDuration = int(gjson.GetBytes(body, "duration").Int())
	if gjson.GetBytes(body, "content").IsArray() {
		request.VividAITier = gjson.GetBytes(body, "resolution").String()
		duration := gjson.GetBytes(body, "duration")
		request.VividAIDuration = duration.Int()
		if duration.Exists() && (duration.Type != gjson.Number || duration.Float() != float64(duration.Int()) || duration.Int() <= 0) {
			request.VividAIDuration = -1
		}
		request.LongXiaVideo, request.LongXiaResolution, request.LongXiaDuration = true, request.VividAITier, request.VividAIDuration
		if resolution := gjson.GetBytes(body, "resolution"); resolution.Exists() && (resolution.Type != gjson.String || resolution.String() == "") {
			request.LongXiaResolution = "unknown"
		}
	} else {
		parsed := &OpenAIImagesRequest{N: 1, Model: "price", Prompt: "price", Size: gjson.GetBytes(body, "size").String(), Quality: gjson.GetBytes(body, "quality").String()}
		if vivid, err := vividAIImageRequest(parsed, "price"); err == nil {
			request.VividAITier = vivid.Quality
		} else {
			request.VividAITier = "unknown"
		}
	}
	request.KongfangTier = kongfangRequestTier(body)
	request.KongfangBillingTier = kongfangLocalBillingTier(body)
	if tier := gjson.GetBytes(body, "service_tier").String(); tier != "" && tier != "default" && tier != "auto" {
		request.UnpricedServiceTier = true
	}
	if gjson.GetBytes(body, "speed").String() == "fast" {
		request.UnpricedServiceTier = true
	}
	for _, path := range []string{"size", "image_size", "resolution", "generationConfig.imageConfig.imageSize", "generation_config.image_config.image_size"} {
		if value := gjson.GetBytes(body, path); value.Exists() {
			request.Tier = siteRequestTier(value.String())
			break
		}
	}
	return context.WithValue(ctx, siteRequestKey{}, request)
}
func WithSiteImageSize(ctx context.Context, size string) context.Context {
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	request.Tier = siteRequestTier(size)
	if request.VividAITier == "" {
		request.VividAITier = request.Tier
		if request.VividAITier == "" {
			request.VividAITier = "1K"
		}
	}
	if request.KongfangTier == "" {
		request.KongfangTier = kongfangSizeTier(size)
	}
	return context.WithValue(ctx, siteRequestKey{}, request)
}
func siteRequestTier(size string) string {
	if strings.TrimSpace(size) == "" || size == "auto" {
		return ""
	}
	if tier, ok := ClassifyImageBillingTier(size); ok {
		return tier
	}
	return "unknown"
}
func (a *Account) IsSiteManaged() bool {
	if a == nil {
		return false
	}
	id, _ := a.Extra[SiteBindingCredentialKey].(string)
	return id != "" || a.GetCredential(SiteBindingCredentialKey) != ""
}
func (a *Account) SitePolicy() (SiteAccountPolicy, bool) {
	var p SiteAccountPolicy
	if !a.IsSiteManaged() {
		return p, false
	}
	raw, err := json.Marshal(a.Extra[SitePolicyExtraKey])
	if err != nil {
		return p, true
	}
	if json.Unmarshal(raw, &p) != nil || p.BindingID == "" || p.BindingID != a.GetCredential(SiteBindingCredentialKey) {
		return SiteAccountPolicy{}, true
	}
	return p, true
}
func siteTierReason(p SiteAccountPolicy, key string, now time.Time) string {
	if !p.Enabled {
		return "site_disabled"
	}
	if !p.ManualPrice && (p.FreshUntil.IsZero() || !now.Before(p.FreshUntil)) {
		return "site_price_expired"
	}
	if p.Reason != "" {
		return "site_price_unknown"
	}
	var price *SitePriceTier
	var limit *SiteTierLimit
	for i := range p.Tiers {
		if p.Tiers[i].Key == key {
			price = &p.Tiers[i]
			break
		}
	}
	for i := range p.Limits {
		if p.Limits[i].Key == key {
			limit = &p.Limits[i]
			break
		}
	}
	if price == nil || limit == nil || len(price.Prices) == 0 || price.Reason != "" || price.Unit != limit.Unit || limit.Reason != "" {
		return "site_price_unknown"
	}
	if !limit.Enabled {
		return "site_tier_disabled"
	}
	hasPositivePrice, equalPrice := false, false
	for component, v := range price.Prices {
		cap, ok := limit.Limits[component]
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || math.IsNaN(cap) || math.IsInf(cap, 0) || cap < 0 {
			return "site_price_unknown"
		}
		tolerance := 1e-12 * math.Max(1, math.Abs(cap))
		if v-cap > tolerance {
			return "site_price_exceeded"
		}
		// Zero-priced optional components (e.g. cache writes) do not block a
		// discounted paid component. An entirely free tier is still equal.
		if v > 0 || cap > 0 {
			hasPositivePrice = true
			equalPrice = equalPrice || math.Abs(v-cap) <= tolerance
		}
	}
	if !limit.AllowEqualPriceScheduling && (equalPrice || !hasPositivePrice) {
		return "site_price_equal"
	}
	return ""
}
func (a *Account) siteHasEligibleTier() bool {
	p, managed := a.SitePolicy()
	if !managed {
		return true
	}
	// Auto ceilings can recover after a local price edit without an upstream scan.
	if p.LocalGroupID > 0 {
		return p.Enabled && (p.ManualPrice || time.Now().Before(p.FreshUntil)) && p.Reason == "" && len(p.Tiers) > 0
	}
	for _, tier := range p.Tiers {
		if siteTierReason(p, tier.Key, time.Now()) == "" {
			return true
		}
	}
	return false
}

// SitePriceVeto is independent of the optional group profit control. A sticky
// session, retry or unavailable fallback can never suppress this gate.
func SitePriceVeto(ctx context.Context, a *Account) (bool, string) {
	if a == nil {
		return false, ""
	}
	if id, ok := ctx.Value(siteAcceptedVideoAccountKey{}).(int64); ok && id == a.ID && (a.IsVividAI() || a.IsLongXia()) {
		return false, ""
	}
	p, managed := a.SitePolicy()
	if !managed {
		return false, ""
	}
	pricing, _ := ctx.Value(sitePricingKey{}).(*UpstreamSitePricing)
	if p.LocalGroupID == 0 && pricing != nil && len(a.GroupIDs) == 1 {
		p.LocalGroupID = a.GroupIDs[0]
	}
	if pricing != nil && p.LocalGroupID <= 0 {
		return true, "site_price_unknown"
	}
	if p.LocalGroupID > 0 {
		p = pricing.apply(ctx, p, true)
	}
	request, _ := ctx.Value(siteRequestKey{}).(SitePriceRequest)
	if request.UnpricedServiceTier {
		return true, "site_price_unknown"
	}
	if p.SiteKind == "kongfang" {
		return kongfangPriceVeto(p, request)
	}
	if p.SiteKind == "wuzu" {
		return wuzuPriceVeto(p, request)
	}
	if p.SiteKind == "vividai" {
		return vividAISitePriceVeto(p, request)
	}
	if p.LongXia != nil {
		return longXiaSitePriceVeto(p, request)
	}
	if isGrokVideoGenerationModel(p.UpstreamModel) || isGrokVideoGenerationModel(p.LocalModel) {
		resolution := request.VideoResolution
		if resolution == "" {
			resolution = VideoBillingResolution480P
		}
		for _, tier := range p.Tiers {
			if tier.Key == "default" && tier.Unit == "USD/request" {
				resolution = "default"
				break
			}
		}
		reason := siteTierReason(p, resolution, time.Now())
		return reason != "", reason
	}
	for _, tier := range p.Tiers {
		if tier.Key == "default" {
			reason := siteTierReason(p, "default", time.Now())
			return reason != "", reason
		}
	}
	if request.Tier != "" {
		reason := siteTierReason(p, request.Tier, time.Now())
		return reason != "", reason
	}
	// Missing/automatic image size may produce any supported resolution. Require
	// all three ceilings; never silently price it as the cheapest size.
	for _, key := range []string{"1K", "2K", "4K"} {
		if reason := siteTierReason(p, key, time.Now()); reason != "" {
			return true, reason
		}
	}
	return false, ""
}
func CheckSitePriceBeforeSend(ctx context.Context, a *Account, repo AccountRepository) error {
	if !a.IsSiteManaged() {
		return nil
	}
	latest := a
	if repo != nil {
		var err error
		latest, err = repo.GetByID(ctx, a.ID)
		if err != nil || latest == nil {
			return errors.New("site price unavailable")
		}
	}
	if !latest.IsSiteManaged() || latest.GetCredential(SiteBindingCredentialKey) != a.GetCredential(SiteBindingCredentialKey) || !latest.IsActive() || !latest.Schedulable {
		return &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"message":"Managed upstream is disabled"}}`)}
	}
	if vetoed, _ := SitePriceVeto(ctx, latest); vetoed {
		return &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"message":"Managed upstream price is unavailable or exceeds its limit"}}`)}
	}
	return nil
}

// This decorator is installed only for managed accounts. It checks authoritative
// account data at every actual network attempt, including an internal retry.
type sitePriceHTTPUpstream struct {
	HTTPUpstream
	repo    AccountRepository
	account *Account
}

func siteCheckedUpstream(upstream HTTPUpstream, repo AccountRepository, account *Account) HTTPUpstream {
	if !account.IsSiteManaged() {
		return upstream
	}
	return &sitePriceHTTPUpstream{upstream, repo, account}
}
func (s *sitePriceHTTPUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	if err := s.check(req); err != nil {
		return nil, err
	}
	markSiteForwardStarted(req.Context())
	return s.HTTPUpstream.Do(req, proxy, id, concurrency)
}
func (s *sitePriceHTTPUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if err := s.check(req); err != nil {
		return nil, err
	}
	markSiteForwardStarted(req.Context())
	return s.HTTPUpstream.DoWithTLS(req, proxy, id, concurrency, profile)
}

func (s *sitePriceHTTPUpstream) check(req *http.Request) error {
	// Retrieving an already-paid result or model metadata must remain possible.
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return nil
	}
	if p, ok := s.account.SitePolicy(); ok && p.SiteKind == "wuzu" {
		if err := prepareWuzuRequest(req, p.Wuzu); err != nil {
			return err
		}
	}
	if p, ok := s.account.SitePolicy(); ok && p.SiteKind == "kongfang" {
		if err := prepareKongfangRequest(req); err != nil {
			return err
		}
	}
	return CheckSitePriceBeforeSend(req.Context(), s.account, s.repo)
}

// Public image aliases are accepted only when explicitly registered by a
// managed image binding in the authenticated group.
func (s *OpenAIGatewayService) siteImageAliasAllowed(ctx context.Context, model string) bool {
	if s == nil || s.accountRepo == nil {
		return false
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil {
		return false
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, &group.ID, []string{PlatformOpenAI}, false)
	if err != nil {
		return false
	}
	for i := range accounts {
		p, managed := accounts[i].SitePolicy()
		if managed && p.Image && p.LocalModel == model {
			return true
		}
	}
	return false
}
