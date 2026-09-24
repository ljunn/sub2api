package service

import (
	"context"
	"errors"
	"math"
)

// Manual prices are final USD purchase costs, independent of upstream rates.
// Keep them separate from the catalogue so sync and temporary removal preserve
// the administrator's choice. Removing an override restores the synced price.
type SiteManualPrice struct {
	GroupID     string             `json:"group_id"`
	Model       string             `json:"model"`
	BillingMode string             `json:"billing_mode"`
	Prices      map[string]float64 `json:"prices"`
}

type SiteManualPriceInput struct {
	SiteManualPrice
	Automatic bool `json:"automatic"`
}

func (p SiteManualPrice) tiers() ([]SitePriceTier, error) {
	allowed := map[string]bool{}
	required := []string{}
	switch p.BillingMode {
	case "image":
		for _, k := range []string{"1K", "2K", "4K"} {
			allowed[k] = true
		}
	case "video":
		for _, k := range []string{"480p", "720p", "1080p"} {
			allowed[k] = true
		}
	case "per_request":
		allowed["request"] = true
		required = []string{"request"}
	case "token":
		for _, k := range []string{"input_price", "output_price", "cache_read_price", "cache_write_price", "cache_write_1h_price", "image_input_price", "image_output_price"} {
			allowed[k] = true
		}
		required = []string{"input_price", "output_price"}
	default:
		return nil, errors.New("请选择计费方式")
	}
	if len(p.Prices) == 0 {
		return nil, errors.New("请填写采购价")
	}
	for k, v := range p.Prices {
		if !allowed[k] || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil, errors.New("采购价必须是非负有效数字")
		}
	}
	for _, k := range required {
		if _, ok := p.Prices[k]; !ok {
			return nil, errors.New("请填写完整的采购价")
		}
	}
	if p.BillingMode == "image" || p.BillingMode == "video" {
		out := []SitePriceTier{}
		keys, unit, component := []string{"1K", "2K", "4K"}, "USD/image", "request"
		if p.BillingMode == "video" {
			keys, unit, component = []string{"480p", "720p", "1080p"}, "USD/second", "second"
		}
		for _, k := range keys {
			t := SitePriceTier{Key: k, Unit: unit, Prices: map[string]float64{}, Note: "手动采购价"}
			if v, ok := p.Prices[k]; ok {
				t.Prices[component] = v
			} else {
				t.Reason = "未填写此分辨率采购价"
			}
			out = append(out, t)
		}
		return out, nil
	}
	prices := map[string]float64{}
	for k, v := range p.Prices {
		prices[k] = v
	}
	unit := "USD/request"
	if p.BillingMode == "token" {
		unit = "USD/1M tokens"
		for _, k := range []string{"cache_read_price", "cache_write_price", "cache_write_1h_price", "image_input_price"} {
			if _, ok := prices[k]; !ok {
				prices[k] = prices["input_price"]
			}
		}
		if _, ok := prices["image_output_price"]; !ok {
			prices["image_output_price"] = prices["output_price"]
		}
	}
	return []SitePriceTier{{Key: "default", Unit: unit, Prices: prices, Note: "手动采购价"}}, nil
}

func siteModelWithPrice(site *UpstreamSite, model SiteModel) SiteModel {
	if model.LongXia != nil {
		return model // Generic token/request overrides cannot describe these SKUs.
	}
	for i := range site.ManualPrices {
		price := &site.ManualPrices[i]
		if price.GroupID != model.GroupID || price.Model != model.Model {
			continue
		}
		if price.BillingMode == "image" && isGrokVideoGenerationModel(model.Model) {
			model.ManualPrice = price
			model.Image = false
			model.Tiers = []SitePriceTier{}
			model.Reason = "视频模型保存了图片采购价，请重新填写视频价格或恢复自动价格"
			return model
		}
		tiers, err := price.tiers()
		if err != nil {
			model.Reason = err.Error()
			return model
		}
		model.ManualPrice = price
		model.Tiers = tiers
		model.Reason = ""
		model.PriceSyncError = ""
		model.Image = price.BillingMode == "image" || (price.BillingMode != "video" && model.Image)
		break
	}
	return model
}

// Project effective prices only at the API boundary; never overwrite the
// original synced catalogue, which remains available when returning to auto.
func ApplySiteManualPrices(site *UpstreamSite) {
	models := make([]SiteModel, len(site.Models))
	for i, model := range site.Models {
		models[i] = siteModelWithPrice(site, model)
	}
	site.Models = models
}

func (s *UpstreamSiteService) SaveManualPrice(ctx context.Context, id string, input SiteManualPriceInput) (*UpstreamSite, error) {
	if !input.Automatic {
		if _, err := input.tiers(); err != nil {
			return nil, err
		}
	}
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if findSiteModel(site, input.GroupID, input.Model) == nil {
		return nil, errors.New("模型不在同步目录中，请先同步站点")
	}
	if !input.Automatic && input.BillingMode == "image" && isGrokVideoGenerationModel(input.Model) {
		return nil, errors.New("Grok 视频不能按图片计价，请选择按视频时长或按次")
	}
	if !input.Automatic && findSiteModel(site, input.GroupID, input.Model).LongXia != nil {
		return nil, errors.New("LongXia 视频请使用自动同步的 SKU 单价和计费单位")
	}
	if site.Kind == "vividai" && !input.Automatic && !findSiteModel(site, input.GroupID, input.Model).Image {
		return nil, errors.New("VividAI 视频请使用自动积分价格，并在站点设置填写积分美元换算倍率")
	}
	prices := make([]SiteManualPrice, 0, len(site.ManualPrices)+1)
	for _, price := range site.ManualPrices {
		if price.GroupID != input.GroupID || price.Model != input.Model {
			prices = append(prices, price)
		}
	}
	if !input.Automatic {
		prices = append(prices, input.SiteManualPrice)
	}
	site.ManualPrices = prices
	if err = s.repo.Save(ctx, site); err != nil {
		return nil, err
	}
	if err = s.updatePolicies(ctx, site); err != nil {
		return nil, err
	}
	return site, nil
}
