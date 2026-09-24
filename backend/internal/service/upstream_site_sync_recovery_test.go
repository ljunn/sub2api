package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteSyncFinishesAfterCallerDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			cancel() // The browser reloads while the accepted sync is in flight.
			fmt.Fprint(w, `{"data":{"id":1}}`)
		case "/api/v1/model-plaza":
			fmt.Fprint(w, `{"data":{"groups":[{"id":1,"rate_multiplier":1,"models":[{"name":"image","pricing":{"billing_mode":"per_request","per_request_price":0.1}}]}]}}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{}}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	site, err := svc.Save(ctx, "", SiteInput{Name: "disconnected", Kind: "sub2api", BaseURL: server.URL, AuthMode: "token", AccessToken: "test", Enabled: true})
	require.NoError(t, err)
	result, err := svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, "connected", result.Status)
	require.Empty(t, repo.sites[site.ID].Error)
	require.Len(t, repo.sites[site.ID].Models, 1)
	_, err = svc.Sync(ctx, site.ID)
	require.ErrorIs(t, err, context.Canceled, "already canceled callers must not start more work")
}

func TestUpstreamSiteBalanceFailureKeepsKeysAndRecovers(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			balanceEmpty, modelsCalls, keyCreates := true, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/me":
					fmt.Fprint(w, `{"data":{"id":1}}`)
				case "/api/v1/model-plaza", "/api/v1/public/model-pricing":
					w.WriteHeader(404)
				case "/api/v1/groups/available":
					fmt.Fprint(w, `{"data":[{"id":1,"name":"Images","platform":"openai","rate_multiplier":1},{"id":2,"platform":"openai","rate_multiplier":1}]}`)
				case "/api/v1/groups/rates":
					fmt.Fprint(w, `{"data":{}}`)
				case "/api/v1/channels/available":
					fmt.Fprint(w, `{"data":[]}`)
				case "/api/v1/keys":
					if r.Method != http.MethodGet {
						keyCreates++
						w.WriteHeader(500)
						return
					}
					fmt.Fprintf(w, `{"data":{"items":[{"group_id":%s,"status":"active","key":"valid-key"}]}}`, r.URL.Query().Get("group_id"))
				case "/v1/models":
					modelsCalls++
					if balanceEmpty {
						w.WriteHeader(403)
						fmt.Fprint(w, `{"code":"INSUFFICIENT_BALANCE","message":"private detail must stay private"}`)
						return
					}
					fmt.Fprint(w, `{"data":[{"id":"model"}]}`)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			svc, repo, _ := siteTestService()
			site, err := svc.Save(context.Background(), "", SiteInput{Name: "balance", Kind: "sub2api", BaseURL: server.URL, AuthMode: "token", AccessToken: "test", Enabled: true})
			require.NoError(t, err)
			if cached {
				c, err := svc.credentials(site)
				require.NoError(t, err)
				c.Keys = map[string]string{"catalog-" + site.ID + "-1": "valid-key"}
				require.NoError(t, svc.saveSecret(context.Background(), site, c))
			}
			site, err = svc.Sync(context.Background(), site.ID)
			require.NoError(t, err)
			require.Equal(t, "error", site.Status)
			require.Contains(t, site.Error, "上游账户余额不足")
			require.NotContains(t, site.Error, "private")
			require.Nil(t, site.LastSuccess)
			require.Equal(t, 1, modelsCalls, "account balance failure must stop scanning other keys and groups")
			require.Zero(t, keyCreates)
			if cached {
				c, err := svc.credentials(repo.sites[site.ID])
				require.NoError(t, err)
				require.Equal(t, "valid-key", c.Keys["catalog-"+site.ID+"-1"])
			}
			balanceEmpty = false
			site, err = svc.Sync(context.Background(), site.ID)
			require.NoError(t, err)
			require.Equal(t, "connected", site.Status)
			require.Empty(t, site.Error)
			require.Len(t, site.Models, 2)
		})
	}
}
