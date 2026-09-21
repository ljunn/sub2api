//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func siteSchedulerAccount(id int64, price float64) Account {
	policy := SiteAccountPolicy{BindingID: "binding", LocalModel: "gpt-image-1", UpstreamModel: "renamed-upstream", Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: []SitePriceTier{}, Limits: []SiteTierLimit{}}
	for _, tier := range []string{"1K", "2K", "4K"} {
		cost := 0.1
		if tier == "2K" {
			cost = price
		}
		policy.Tiers = append(policy.Tiers, SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{"request": cost}})
		policy.Limits = append(policy.Limits, SiteTierLimit{Key: tier, Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 0.2}})
	}
	raw, _ := json.Marshal(policy)
	var value any
	_ = json.Unmarshal(raw, &value)
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2, Credentials: map[string]any{SiteBindingCredentialKey: "binding"}, Extra: map[string]any{SitePolicyExtraKey: value}}
}

func TestUpstreamSiteSchedulerFiltersPerResolutionWithoutProfitControl(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			account := siteSchedulerAccount(31, 0.4)
			account.Extra["openai_passthrough"] = true
			require.False(t, account.IsModelSupported("different-model"))
			require.Equal(t, "renamed-upstream", account.GetMappedModel("channel-mapped-alias"))
			svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}, cfg: &config.Config{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
			groupID := int64(7)
			selectSize := func(size string, allowed bool) {
				ctx := WithSiteImageSize(context.Background(), size)
				selected, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "gpt-image-1", nil, OpenAIUpstreamTransportAny, false)
				if !allowed {
					require.Error(t, err)
					require.Nil(t, selected)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, selected)
				require.Equal(t, account.ID, selected.Account.ID)
				if selected.ReleaseFunc != nil {
					selected.ReleaseFunc()
				}
			}
			selectSize("1K", true)
			selectSize("2K", false)
			selectSize("", false)
			svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{siteSchedulerAccount(31, 0.15)}}
			selectSize("2K", true)
		})
	}
}

func TestUpstreamSiteEveryNetworkAttemptChecksLatestPrice(t *testing.T) {
	selected := siteSchedulerAccount(31, 0.1)
	repo := &siteTestAccounts{accounts: map[int64]*Account{31: &selected}}
	transport := &pluginRoutingHTTPUpstream{}
	guarded := siteCheckedUpstream(transport, repo, &selected)
	ctx := WithSiteImageSize(context.Background(), "2K")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/images/generations", nil)
	require.NoError(t, err)
	response, err := guarded.Do(request, "", 31, 2)
	require.NoError(t, err)
	response.Body.Close()
	repriced := siteSchedulerAccount(31, 0.5)
	repo.accounts[31] = &repriced
	_, err = guarded.DoWithTLS(request, "", 31, 2, nil)
	require.Error(t, err)
	require.Equal(t, 1, transport.doCalls, "a retry must not spend at the old cached price")
	request.Method = http.MethodGet
	response, err = guarded.Do(request, "", 31, 2)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 2, transport.doCalls, "already-paid result downloads remain available")
}
