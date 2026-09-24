//go:build unit

package service

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func managedGrokAccountForUpdate() *Account {
	return &Account{ID: 55, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, GroupIDs: []int64{12},
		Credentials: map[string]any{
			"api_key": "kept-secret", "base_url": "https://relay.example", "model_mapping": map[string]any{"grok-imagine-video": "grok-imagine-video"},
			"pool_mode": true, "pool_mode_retry_count": 0, SiteBindingCredentialKey: "binding", siteVideoAPIFormatCredentialKey: "openai",
		},
		Extra: map[string]any{SiteBindingCredentialKey: "binding", "upstream_site_id": "site", SitePolicyExtraKey: map[string]any{"binding_id": "binding", "enabled": true, "video_api_format": "openai"}},
	}
}

func TestUpdateSiteManagedAccountGrokFormat(t *testing.T) {
	for _, mode := range []string{"partial", "editor"} {
		t.Run(mode, func(t *testing.T) {
			account := managedGrokAccountForUpdate()
			before := maps.Clone(account.Credentials)
			policy := account.Extra[SitePolicyExtraKey]
			repo := &updateAccountCredsRepoStub{account: account}
			svc := &adminServiceImpl{accountRepo: repo}
			for _, format := range []string{"openai", "xai", "auto"} {
				input := &UpdateAccountInput{Credentials: map[string]any{GrokMediaAPIFormatCredentialKey: format}}
				if mode == "editor" {
					// The UI sends redacted, possibly stale credentials and policy.
					raw, err := json.Marshal(account.Credentials)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(raw, &input.Credentials))
					delete(input.Credentials, "api_key")
					delete(input.Credentials, siteVideoAPIFormatCredentialKey)
					input.Credentials[GrokMediaAPIFormatCredentialKey] = format
					groups := []int64{12}
					input.GroupIDs = &groups
					input.Extra = map[string]any{SitePolicyExtraKey: map[string]any{"tiers": "stale"}, "upstream_site_id": "stale"}
				}
				updated, err := svc.UpdateAccount(context.Background(), account.ID, input)
				require.NoError(t, err)
				require.Equal(t, format, updated.GetCredential(GrokMediaAPIFormatCredentialKey))
				for key, value := range before {
					require.Equal(t, value, updated.Credentials[key], key)
				}
				require.Equal(t, policy, updated.Extra[SitePolicyExtraKey])
				require.Equal(t, "site", updated.Extra["upstream_site_id"])
				require.Equal(t, []int64{12}, updated.GroupIDs)
			}
			require.Equal(t, 3, repo.updateCalls)
		})
	}
}

func TestUpdateSiteManagedAccountRejectsProtectedChangesAsBadRequest(t *testing.T) {
	for name, input := range map[string]*UpdateAccountInput{
		"key":            {Credentials: map[string]any{"api_key": "replacement"}},
		"url":            {Credentials: map[string]any{"base_url": "https://other.example"}},
		"mapping":        {Credentials: map[string]any{"model_mapping": map[string]any{"grok-imagine-video": "another"}}},
		"binding":        {Credentials: map[string]any{SiteBindingCredentialKey: "other"}},
		"cached format":  {Credentials: map[string]any{siteVideoAPIFormatCredentialKey: "xai"}},
		"group":          {GroupIDs: &[]int64{13}},
		"type":           {Type: AccountTypeOAuth},
		"invalid format": {Credentials: map[string]any{GrokMediaAPIFormatCredentialKey: "gpt"}},
		"null format":    {Credentials: map[string]any{GrokMediaAPIFormatCredentialKey: nil}},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &updateAccountCredsRepoStub{account: managedGrokAccountForUpdate()}
			before, err := json.Marshal(repo.account)
			require.NoError(t, err)
			_, err = (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), 55, input)
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Zero(t, repo.updateCalls)
			after, err := json.Marshal(repo.account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}
