package service

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// The account editor submits unchanged groups and redacted credentials along
// with local settings. Accept those echoes, but only allow the Grok protocol
// override to change; the site still owns the key, URL, mapping and bindings.
func normalizeSiteManagedAccountUpdate(account *Account, input *UpdateAccountInput) error {
	managedError := func() error {
		return infraerrors.BadRequest("SITE_MANAGED_ACCOUNT", "站点托管账号的凭据、模型映射与分组请在站点管理中维护；Grok 上游格式可在账号中修改")
	}
	if input.Type != "" && input.Type != account.Type {
		return managedError()
	}
	if input.GroupIDs != nil {
		current, incoming := slices.Clone(account.GroupIDs), slices.Clone(*input.GroupIDs)
		slices.Sort(current)
		slices.Sort(incoming)
		if !slices.Equal(current, incoming) {
			return managedError()
		}
	}
	format, hasFormat := "", false
	for key, value := range input.Credentials {
		if key == GrokMediaAPIFormatCredentialKey && account.Platform == PlatformGrok && account.Type == AccountTypeAPIKey {
			format, _ = value.(string)
			if format != "auto" && format != "xai" && format != GrokMediaAPIFormatOpenAI {
				return infraerrors.BadRequest("INVALID_GROK_MEDIA_API_FORMAT", "Grok 上游格式必须为 auto、xai 或 openai")
			}
			hasFormat = true
			continue
		}
		stored, exists := account.Credentials[key]
		before, beforeErr := json.Marshal(stored)
		after, afterErr := json.Marshal(value)
		if !exists || beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
			return managedError()
		}
	}
	// A partial protocol update must preserve every site-owned credential, even
	// if an older editor did not know about it. Do not rebind unchanged groups.
	input.GroupIDs = nil
	input.Credentials = nil
	if hasFormat {
		input.Credentials = maps.Clone(account.Credentials)
		input.Credentials[GrokMediaAPIFormatCredentialKey] = format
	}
	return nil
}
