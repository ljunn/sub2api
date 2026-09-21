package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiConfiguredModelsListsAllBindingsWithoutUpstreamLeak(t *testing.T) {
	for _, allowlist := range []bool{false, true} {
		t.Run(map[bool]string{false: "all bindings", true: "group allowlist"}[allowlist], func(t *testing.T) {
			id := int64(11)
			accounts := []service.Account{}
			for i, model := range []string{"gemini-3.1-flash-image-preview", "gemini-3-pro-image-preview", "gemini-3-pro-image-preview"} {
				accounts = append(accounts, service.Account{ID: int64(i + 1), Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
					Credentials: map[string]any{service.SiteBindingCredentialKey: model, "api_key": "unused", "model_mapping": map[string]any{"stale-alias": "upstream-model"}},
					Extra:       map[string]any{service.SitePolicyExtraKey: map[string]any{"binding_id": model, "local_model": model, "upstream_model": "upstream-name"}},
				})
			}
			repo := &geminiAllowlistAccountRepoStub{gatewayModelsAccountRepoStub: gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{id: accounts}}}
			// No HTTP transport: a fully configured group must use its local names.
			h := &GatewayHandler{geminiCompatService: service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, nil, nil, nil)}
			key := &service.APIKey{GroupID: &id, Group: &service.Group{ID: id, Platform: service.PlatformGemini,
				ModelAllowlist: service.GroupModelAllowlist{Enabled: allowlist, Models: []string{"gemini-3-pro-image-preview"}}}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
			c.Set(string(middleware.ContextKeyAPIKey), key)
			h.GeminiV1BetaListModels(c)
			require.Equal(t, 200, rec.Code)
			var got gemini.ModelsListResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			names := []string{}
			for _, m := range got.Models {
				names = append(names, m.Name)
			}
			want := []string{"models/gemini-3-pro-image-preview", "models/gemini-3.1-flash-image-preview"}
			if allowlist {
				want = want[:1]
			}
			require.Equal(t, want, names)
			for _, model := range []string{"gemini-3-pro-image-preview", "gemini-3.1-flash-image", "upstream-name"} {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/"+model, nil)
				c.Params = gin.Params{{Key: "model", Value: model}}
				c.Set(string(middleware.ContextKeyAPIKey), key)
				h.GeminiV1BetaGetModel(c)
				if model == "gemini-3-pro-image-preview" {
					require.Equal(t, 200, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), "models/"+model)
				} else {
					require.Equal(t, 404, rec.Code, rec.Body.String())
				}
			}
		})
	}
}
