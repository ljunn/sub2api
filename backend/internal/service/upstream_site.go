package service

import (
	"context"
	"time"
)

const SitePolicyExtraKey = "upstream_site_policy"
const SiteBindingCredentialKey = "upstream_site_binding_id"

// A separate named dependency prevents accidentally using the TOTP encryptor,
// whose unconfigured key is ephemeral on some existing installations.
type UpstreamSiteSecretEncryptor interface{ SecretEncryptor }

// Prices and limits use USD per request/image/second or per million tokens. Missing is
// unknown, never zero. A tier can carry several independently bounded components.
type SitePriceTier struct {
	MaxDurationSeconds int64 `json:"max_duration_seconds,omitempty"`
	// VividAI video bills 1000 output units per upstream credit. This is the
	// usage per tier unit (per second for video).
	CreditUnits float64            `json:"credit_units,omitempty"`
	Note        string             `json:"note,omitempty"`
	Key         string             `json:"key"`
	Unit        string             `json:"unit"`
	Prices      map[string]float64 `json:"prices"`
	Reason      string             `json:"reason,omitempty"`
}

type SiteModel struct {
	VideoAPIFormat string            `json:"video_api_format,omitempty"`
	LongXia        *SiteLongXiaModel `json:"longxia,omitempty"`
	Wuzu           *WuzuModelConfig  `json:"wuzu,omitempty"`
	VividAI        *SiteVividAIModel `json:"vividai,omitempty"`
	ManualPrice    *SiteManualPrice  `json:"manual_price,omitempty"`
	DiscoveryID    string            `json:"discovery_id,omitempty"`
	DiscoveredAt   *time.Time        `json:"discovered_at,omitempty"`
	Unread         bool              `json:"unread,omitempty"`
	Image          bool              `json:"image"`
	GroupID        string            `json:"group_id"`
	GroupName      string            `json:"group_name"`
	Model          string            `json:"model"`
	Platform       string            `json:"platform"`
	Tiers          []SitePriceTier   `json:"tiers"`
	Reason         string            `json:"reason,omitempty"`
}

type SiteTierLimit struct {
	AllowEqualPriceScheduling bool               `json:"allow_equal_price_scheduling"`
	Selling                   map[string]float64 `json:"selling,omitempty"`
	Reason                    string             `json:"reason,omitempty"`
	Key                       string             `json:"key"`
	Unit                      string             `json:"unit"`
	Enabled                   bool               `json:"enabled"`
	Limits                    map[string]float64 `json:"limits"`
}

type SiteBinding struct {
	TrafficSupport *SiteTrafficSupport `json:"traffic_support,omitempty"`
	PriceTiers     []SitePriceTier     `json:"price_tiers,omitempty"`
	ID             string              `json:"id"`
	GroupID        string              `json:"group_id"`
	Model          string              `json:"model"`
	LocalGroupID   int64               `json:"local_group_id"`
	LocalModel     string              `json:"local_model"`
	Platform       string              `json:"platform"`
	AccountID      int64               `json:"account_id"`
	Enabled        bool                `json:"enabled"`
	Limits         []SiteTierLimit     `json:"limits"`
	Status         string              `json:"status"`
	Error          string              `json:"error,omitempty"`
}

type UpstreamSite struct {
	ManualPrices       []SiteManualPrice   `json:"manual_prices,omitempty"`
	ModelCatalogue     *SiteModelCatalogue `json:"model_catalogue,omitempty"`
	Balance            *SiteBalance        `json:"balance,omitempty"`
	Concurrency        *SiteConcurrency    `json:"concurrency,omitempty"`
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	BaseURL            string              `json:"base_url"`
	Kind               string              `json:"kind"`
	AuthMode           string              `json:"auth_mode"`
	Username           string              `json:"username"`
	UserID             int64               `json:"user_id"`
	Enabled            bool                `json:"enabled"`
	Status             string              `json:"status"`
	Error              string              `json:"error,omitempty"`
	Warnings           []string            `json:"warnings,omitempty"`
	LastAttempt        *time.Time          `json:"last_attempt,omitempty"`
	LastSuccess        *time.Time          `json:"last_success,omitempty"`
	NextSync           *time.Time          `json:"next_sync,omitempty"`
	Models             []SiteModel         `json:"models"`
	Bindings           []SiteBinding       `json:"bindings"`
	History            []SitePriceChange   `json:"history"`
	CreditUSD          float64             `json:"usd_per_credit,omitempty"`
	BalanceUnitsPerUSD float64             `json:"balance_units_per_usd,omitempty"`
	Secret             string              `json:"-"`
}

type SitePriceChange struct {
	At      time.Time       `json:"at"`
	GroupID string          `json:"group_id"`
	Model   string          `json:"model"`
	Before  []SitePriceTier `json:"before"`
	After   []SitePriceTier `json:"after"`
}

type SiteCredentials struct {
	SubjectID    string            `json:"subject_id,omitempty"`
	Password     string            `json:"password,omitempty"`
	AccessToken  string            `json:"access_token,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	Cookies      map[string]string `json:"cookies,omitempty"`
	Keys         map[string]string `json:"keys,omitempty"`
}

type SiteInput struct {
	BalanceUnitsPerUSD *float64 `json:"balance_units_per_usd"`
	CreditUSD          float64  `json:"usd_per_credit,omitempty"`
	Name               string   `json:"name"`
	BaseURL            string   `json:"base_url"`
	Kind               string   `json:"kind"`
	AuthMode           string   `json:"auth_mode"`
	Username           string   `json:"username"`
	UserID             int64    `json:"user_id"`
	Enabled            bool     `json:"enabled"`
	Password           string   `json:"password"`
	AccessToken        string   `json:"access_token"`
	RefreshToken       string   `json:"refresh_token"`
}

type SiteAccountPolicy struct {
	VideoAPIFormat string            `json:"video_api_format,omitempty"`
	LongXia        *SiteLongXiaModel `json:"longxia,omitempty"`
	Wuzu           *WuzuModelConfig  `json:"wuzu,omitempty"`
	VividAI        *SiteVividAIModel `json:"vividai,omitempty"`
	ManualPrice    bool              `json:"manual_price,omitempty"`
	SiteKind       string            `json:"site_kind,omitempty"`
	LocalGroupID   int64             `json:"local_group_id"`
	Image          bool              `json:"image"`
	SiteID         string            `json:"site_id"`
	SiteName       string            `json:"site_name"`
	BindingID      string            `json:"binding_id"`
	LocalModel     string            `json:"local_model"`
	UpstreamModel  string            `json:"upstream_model"`
	Enabled        bool              `json:"enabled"`
	FreshUntil     time.Time         `json:"fresh_until"`
	Tiers          []SitePriceTier   `json:"tiers"`
	Limits         []SiteTierLimit   `json:"limits"`
	Reason         string            `json:"reason,omitempty"`
}

type UpstreamSiteRepository interface {
	List(context.Context) ([]UpstreamSite, error)
	Get(context.Context, string) (*UpstreamSite, error)
	Save(context.Context, *UpstreamSite) error
	Delete(context.Context, string) error
	Lock(context.Context, string) (func(), error)
}
