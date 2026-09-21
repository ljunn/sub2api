package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteSecretsSurviveRestartAndPreview(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.Secret = "persistent-installation-secret-for-test"
	cfg.Totp.EncryptionKey = "ephemeral-first-process"
	first, err := NewUpstreamSiteSecretEncryptor(cfg)
	require.NoError(t, err)
	ciphertext, err := first.Encrypt("upstream-refresh-token")
	require.NoError(t, err)
	require.NotContains(t, ciphertext, "upstream-refresh-token")
	cfg.Totp.EncryptionKey = "ephemeral-preview-or-restarted-process"
	second, err := NewUpstreamSiteSecretEncryptor(cfg)
	require.NoError(t, err)
	plaintext, err := second.Decrypt(ciphertext)
	require.NoError(t, err)
	require.Equal(t, "upstream-refresh-token", plaintext)
	cfg.JWT.Secret = "another-installation-secret"
	other, err := NewUpstreamSiteSecretEncryptor(cfg)
	require.NoError(t, err)
	_, err = other.Decrypt(ciphertext)
	require.Error(t, err)
}
