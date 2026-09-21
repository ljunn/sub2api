package repository

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// JWT.Secret is the persisted installation secret shared by preview and
// production. Derive a domain-separated AES key instead of reusing an optionally
// ephemeral TOTP key. Rotating the installation secret requires re-entering site
// credentials; ciphertext is authenticated by the existing AES-GCM implementation.
func NewUpstreamSiteSecretEncryptor(cfg *config.Config) (service.UpstreamSiteSecretEncryptor, error) {
	if cfg == nil || strings.TrimSpace(cfg.JWT.Secret) == "" {
		return nil, errors.New("upstream sites require a persisted installation secret")
	}
	mac := hmac.New(sha256.New, []byte(cfg.JWT.Secret))
	_, _ = mac.Write([]byte("sub2api/upstream-sites/credentials/v1"))
	return &AESEncryptor{key: mac.Sum(nil)}, nil
}
