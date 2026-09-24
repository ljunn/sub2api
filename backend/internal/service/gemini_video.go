package service

import (
	"context"
	"strings"
)

// Gemini video relays advertise an OpenAI Videos contract independently of
// their native generateContent image/text contract. Official Gemini OAuth and
// API keys without an advertised video endpoint are not relay candidates.
func (a *Account) SupportsGeminiVideoRelay() bool {
	return a != nil && a.Platform == PlatformGemini && a.Type == AccountTypeAPIKey &&
		strings.TrimSpace(a.GetCredential("base_url")) != "" && accountGrokMediaAPIFormat(a) == GrokMediaAPIFormatOpenAI
}

func IsGeminiVideoRelayEndpoint(endpoint GrokMediaEndpoint) bool {
	return endpoint == GrokMediaEndpointVideosGenerations || endpoint == GrokMediaEndpointVideoStatus || endpoint == GrokMediaEndpointVideoContent
}

// The handler has already resolved the owner using group, user and API key.
// Poll the same account even when creation is subsequently paused by pricing.
func (s *OpenAIGatewayService) SelectGeminiVideoRequestAccount(ctx context.Context, groupID *int64, accountID int64) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerSessionSticky}
	if accountID <= 0 || s.accountRepo == nil {
		return nil, decision, ErrNoAvailableAccounts
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || !account.SupportsGeminiVideoRelay() || !account.IsActive() || !s.openAIAccountMatchesSchedulingGroup(account, groupID) {
		return nil, decision, ErrNoAvailableAccounts
	}
	slot, err := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if err != nil {
		return nil, decision, err
	}
	selection := &AccountSelectionResult{Account: account, acceptedSiteVideoTask: true}
	if slot != nil && slot.Acquired {
		selection.Acquired, selection.ReleaseFunc = true, slot.ReleaseFunc
	} else {
		cfg := s.schedulingConfig()
		selection.WaitPlan = &AccountWaitPlan{AccountID: accountID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting}
	}
	decision.StickySessionHit, decision.SelectedAccountID, decision.SelectedAccountType = true, account.ID, account.Type
	return selection, decision, nil
}
