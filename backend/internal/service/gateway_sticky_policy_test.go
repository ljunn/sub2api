//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type stickyPolicyCache struct {
	stubGatewayCache
	reads, writes, refreshes int
}

func (c *stickyPolicyCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	c.reads++
	return c.stubGatewayCache.GetSessionAccountID(ctx, groupID, key)
}

func (c *stickyPolicyCache) SetSessionAccountID(ctx context.Context, groupID int64, key string, id int64, ttl time.Duration) error {
	c.writes++
	return c.stubGatewayCache.SetSessionAccountID(ctx, groupID, key, id, ttl)
}

func (c *stickyPolicyCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	c.refreshes++
	return nil
}

func TestDisableStickySessionsGatewayReselectsAfterFailover(t *testing.T) {
	for _, platform := range []string{PlatformGemini, PlatformAnthropic, PlatformAntigravity} {
		for _, loadBatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/load_batch_%t", platform, loadBatch), func(t *testing.T) {
				cfg := testConfig()
				cfg.Gateway.Scheduling.DisableStickySessions = true
				cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
				group := &Group{ID: 9, Platform: platform, Hydrated: true}
				repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
				for i := int64(1); i <= 2; i++ {
					repo.accounts = append(repo.accounts, Account{ID: i, Platform: platform, Type: AccountTypeAPIKey,
						Priority: int(i), Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{group.ID}})
				}
				for i := range repo.accounts {
					repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
				}
				cache := &stickyPolicyCache{stubGatewayCache: stubGatewayCache{sessionBindings: map[string]int64{"same-client": 2}}}
				svc := &GatewayService{cfg: cfg, accountRepo: repo, cache: cache,
					groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{group.ID: group}},
					concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
				// Even a binding prefetched before this switch must not bypass priority.
				ctx := WithPrefetchedStickySession(context.Background(), 2, group.ID, true)
				for i, excluded := range []map[int64]struct{}{nil, {1: {}}, nil} {
					selection, err := svc.SelectAccountWithLoadAwareness(ctx, &group.ID, "same-client", "", excluded, "", 1)
					require.NoError(t, err)
					require.Equal(t, []int64{1, 2, 1}[i], selection.Account.ID)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, &group.ID, "same-client", selection.Account.ID))
				}
				id, err := svc.GetCachedSessionAccountID(ctx, &group.ID, "same-client")
				require.NoError(t, err)
				require.Zero(t, id)
				require.Zero(t, cache.reads+cache.writes+cache.refreshes)
				require.Equal(t, int64(2), cache.sessionBindings["same-client"])
			})
		}
	}
}

func TestDisableStickySessionsGeminiCompatReselectsAfterFailover(t *testing.T) {
	cfg := testConfig()
	cfg.Gateway.Scheduling.DisableStickySessions = true
	repo := &mockAccountRepoForGemini{accounts: []Account{
		{ID: 1, Platform: PlatformGemini, Priority: 1, Status: StatusActive, Schedulable: true},
		{ID: 2, Platform: PlatformGemini, Priority: 2, Status: StatusActive, Schedulable: true},
	}}
	cache := &stickyPolicyCache{stubGatewayCache: stubGatewayCache{sessionBindings: map[string]int64{"gemini:same-client": 2}}}
	svc := &GeminiMessagesCompatService{cfg: cfg, accountRepo: repo, cache: cache}
	for i, excluded := range []map[int64]struct{}{nil, {1: {}}, nil} {
		account, err := svc.SelectAccountForModelWithExclusions(context.Background(), nil, "same-client", "gemini-2.5-flash", excluded)
		require.NoError(t, err)
		require.Equal(t, []int64{1, 2, 1}[i], account.ID)
	}
	require.Zero(t, cache.reads+cache.writes+cache.refreshes)
}

func TestDisableStickySessionsOpenAIReselectsAfterFailover(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("advanced_%t", advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			cfg := newSchedulerTestOpenAIWSV2Config()
			cfg.Gateway.Scheduling.DisableStickySessions = true
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			cfg.Gateway.OpenAIWS.LBTopK = 1
			cfg.Gateway.OpenAIWS.SchedulerScoreWeights = config.GatewayOpenAIWSSchedulerScoreWeights{Priority: 1}
			cache := &stickyPolicyCache{stubGatewayCache: stubGatewayCache{sessionBindings: map[string]int64{"openai:same-client": 2}}}
			svc := &OpenAIGatewayService{cfg: cfg, cache: cache,
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
					{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
					{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 100, Status: StatusActive, Schedulable: true, Concurrency: 5},
				}},
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced)),
				concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
			}
			ctx := context.Background()
			for i, excluded := range []map[int64]struct{}{nil, {1: {}}, nil} {
				selection, decision, err := svc.SelectAccountWithScheduler(ctx, nil, "", "same-client", "gpt-4", excluded, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Equal(t, []int64{1, 2, 1}[i], selection.Account.ID)
				require.False(t, decision.StickySessionHit)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, nil, "same-client", selection.Account.ID))
			}
			require.NoError(t, svc.refreshStickySessionTTL(ctx, nil, "same-client", time.Hour))
			require.Zero(t, cache.reads+cache.writes+cache.refreshes)
			require.Equal(t, int64(2), cache.sessionBindings["openai:same-client"])
		})
	}
}

func TestDisableStickySessionsDigestFallback(t *testing.T) {
	cfg := testConfig()
	cfg.Gateway.Scheduling.DisableStickySessions = true
	store := NewDigestSessionStore()
	store.Save(1, "client", "a-b", "old-session", 2, "")
	svc := &GatewayService{cfg: cfg, digestStore: store}
	ctx := context.Background()
	_, _, _, found := svc.FindGeminiSession(ctx, 1, "client", "a-b-c")
	require.False(t, found)
	_, _, _, found = svc.FindAnthropicSession(ctx, 1, "client", "a-b-c")
	require.False(t, found)
	require.NoError(t, svc.SaveGeminiSession(ctx, 1, "client", "gemini", "new", 1, "a-b"))
	require.NoError(t, svc.SaveAnthropicSession(ctx, 1, "client", "claude", "new", 1, "a-b"))
	_, _, _, found = store.Find(1, "client", "gemini")
	require.False(t, found)
	_, _, _, found = store.Find(1, "client", "claude")
	require.False(t, found)
	_, id, _, found := store.Find(1, "client", "a-b")
	require.True(t, found)
	require.Equal(t, int64(2), id)
}
