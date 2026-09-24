//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTransparentBackgroundSkipsStickyAccount(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			groupID := int64(90)
			accounts := []Account{}
			for id := int64(1); id <= 2; id++ {
				accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
					Schedulable: true, Priority: int(id), Concurrency: 1, GroupIDs: []int64{groupID},
					Extra: map[string]any{ImagesTransparentBackgroundSupportedExtraKey: id == 2}})
			}
			svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &schedulerTestGatewayCache{},
				cfg: &config.Config{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
			ctx := WithImageBackground(context.Background(), "transparent")
			require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, groupID, "resp_background", 1, time.Hour))
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "resp_background", "", "gpt-5.6", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(2), selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestTransparentBackgroundCompatibility(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{ImagesTransparentBackgroundSupportedExtraKey: false}}
	for _, tc := range []struct {
		body    string
		allowed bool
	}{
		{`{"tools":[{"type":"image_generation","background":"transparent"}]}`, false},
		{`{"tools":[{"type":"function","background":"transparent"}]}`, true},
		{`{"tools":[{"type":"function"},{"type":"image_generation","background":" TRANSPARENT "}]}`, false},
		{`{"tools":[{"type":"image_generation","background":"auto"}]}`, true},
		{`{"tools":[{"type":"image_generation","output_format":"png"}]}`, true},
	} {
		ctx := WithResponsesImageBackground(context.Background(), []byte(tc.body))
		require.Equal(t, tc.allowed, ImageBackgroundRequestAllowed(ctx, account))
		scheduler := &defaultOpenAIAccountScheduler{}
		allowed, reason := scheduler.isAccountRequestCompatibleReason(ctx, account, OpenAIAccountScheduleRequest{RequestedModel: "gpt-5.6"})
		require.Equal(t, tc.allowed, allowed)
		if !allowed {
			require.Equal(t, "transparent_background_unsupported", reason)
		}
		require.Equal(t, tc.allowed, isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, "gpt-5.6", false, ""))
		require.Equal(t, tc.allowed, checkImageBackgroundBeforeSend(ctx, account) == nil)
	}
	ctx := WithImageBackground(context.Background(), "transparent")
	delete(account.Extra, ImagesTransparentBackgroundSupportedExtraKey)
	require.True(t, ImageBackgroundRequestAllowed(ctx, account), "legacy accounts preserve behavior")
	account.Extra[ImagesTransparentBackgroundSupportedExtraKey] = true
	require.True(t, ImageBackgroundRequestAllowed(ctx, account))
}

func TestUpdateManagedAccountTransparentBackground(t *testing.T) {
	account := managedGrokAccountForUpdate()
	repo := &updateAccountCredsRepoStub{account: account}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{ImagesTransparentBackgroundSupportedExtraKey: false}})
	require.NoError(t, err)
	require.False(t, updated.SupportsTransparentBackground())
	require.Equal(t, "kept-secret", updated.GetCredential("api_key"))
	updated, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{"unrelated": true}})
	require.NoError(t, err)
	require.False(t, updated.SupportsTransparentBackground(), "older clients must not erase the veto")
	updated, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{ImagesTransparentBackgroundSupportedExtraKey: true}})
	require.NoError(t, err)
	require.True(t, updated.SupportsTransparentBackground())
	for _, value := range []any{"false", 0, nil} {
		_, err = svc.UpdateAccount(context.Background(), account.ID, &UpdateAccountInput{Extra: map[string]any{ImagesTransparentBackgroundSupportedExtraKey: value}})
		require.Error(t, err)
	}
}
