package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
)

const defaultSiteConcurrency = 1000

// This is the upstream login's published limit, shared by its API keys.
// A missing published limit uses the administrator's requested local default;
// it is not evidence that the upstream has unlimited capacity.
type SiteConcurrency struct {
	Limit     int       `json:"limit"`
	Source    string    `json:"source"`
	CheckedAt time.Time `json:"checked_at"`
}

func siteConcurrencyFromProfile(kind string, profile gjson.Result) (*SiteConcurrency, error) {
	if !profile.IsObject() {
		return nil, errors.New("上游账号资料格式无效，无法读取并发限制")
	}
	fields := []string{"concurrency"}
	if kind == "kongfang" || kind == "newapi" {
		fields = []string{"effective_max_concurrency", "max_concurrency", "concurrency"}
	}
	limit := &SiteConcurrency{Limit: defaultSiteConcurrency, Source: "default", CheckedAt: time.Now().UTC()}
	for _, field := range fields {
		v := profile.Get(field)
		if !v.Exists() || v.Type == gjson.Null {
			continue
		}
		// Effective limits already include VIP or other overrides. Do not cap
		// them with the raw configured limit, nor confuse RPM with concurrency.
		n := v.Float()
		if v.Type != gjson.Number || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > math.MaxInt32 || math.Trunc(n) != n {
			return nil, errors.New("上游账号并发限制无效")
		}
		if n > 0 {
			limit.Limit, limit.Source = int(n), field
		}
		return limit, nil
	}
	return limit, nil
}

func (a *siteAdapter) acceptProfile(profile gjson.Result) error {
	id := profile.Get("id").Int()
	if id > 0 && a.site.UserID > 0 && a.site.UserID != id && len(a.site.Bindings) > 0 {
		return errors.New("登录账号与已有绑定不一致，请新建站点")
	}
	limit, err := siteConcurrencyFromProfile(a.site.Kind, profile)
	if err != nil {
		return err // Keep the last verified limit when the response is invalid.
	}
	if id > 0 {
		a.site.UserID = id
	}
	a.site.Concurrency = limit
	return nil
}

func (a *siteAdapter) loadProfile(ctx context.Context, path string) error {
	result, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return a.acceptProfile(result.Get("data"))
}

func siteAccountConcurrency(site *UpstreamSite) int {
	if site != nil && site.Concurrency != nil && site.Concurrency.Limit > 0 {
		return site.Concurrency.Limit
	}
	return defaultSiteConcurrency
}
