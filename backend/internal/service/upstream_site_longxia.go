package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// LongXia authenticates through New API but has a separate video contract.
type SiteLongXiaModel struct {
	Resolution  string `json:"resolution"`
	DurationMin int64  `json:"duration_min"`
	DurationMax int64  `json:"duration_max"`
}

func parseLongXiaSiteModel(m *SiteModel, row gjson.Result, rate float64) bool {
	if !strings.HasPrefix(m.Model, "LongXia-video-") {
		return false
	}
	caps, known := longXiaModelCapabilities(m.Model)
	videoEndpoint := false
	for _, endpoint := range row.Get("supported_endpoint_types").Array() {
		videoEndpoint = videoEndpoint || endpoint.String() == "openai-video"
	}
	price, priced := siteNumber(row.Get("model_price"))
	if !known || !videoEndpoint || !priced || row.Get("quota_type").Int() != 1 {
		m.Reason = "LongXia 视频能力或计费单价不完整，已阻止调度"
		return true
	}
	m.LongXia = &SiteLongXiaModel{Resolution: caps.resolution, DurationMin: int64(caps.minDuration), DurationMax: int64(caps.maxDuration)}
	unit, component := "USD/request", "request"
	if strings.HasSuffix(m.Model, "-PerSecond") {
		unit, component = "USD/second", "second"
	}
	m.Tiers = []SitePriceTier{{Key: caps.resolution, Unit: unit, MaxDurationSeconds: int64(caps.maxDuration), Prices: map[string]float64{component: price * rate}}}
	return true
}

func longXiaSitePriceVeto(p SiteAccountPolicy, request SitePriceRequest) (bool, string) {
	m := p.LongXia
	if m == nil || !request.LongXiaVideo || (request.LongXiaResolution != "" && request.LongXiaResolution != m.Resolution) {
		return true, "site_price_unknown"
	}
	duration := request.LongXiaDuration
	if duration == 0 {
		duration = 8 // The selected SKU's documented default.
	}
	if duration < m.DurationMin || duration > m.DurationMax {
		return true, "site_price_unknown"
	}
	reason := siteTierReason(p, m.Resolution, time.Now())
	return reason != "", reason
}
