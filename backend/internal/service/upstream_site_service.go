package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type UpstreamSiteService struct {
	balanceSettings SettingRepository
	balanceMailer   siteBalanceMailer
	balanceUsers    UserRepository
	pricing         *UpstreamSitePricing
	repo            UpstreamSiteRepository
	accounts        AccountRepository
	admin           AdminService
	encryptor       SecretEncryptor
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	preview         bool
}

func NewUpstreamSiteService(repo UpstreamSiteRepository, accounts AccountRepository, admin AdminService, encryptor UpstreamSiteSecretEncryptor) *UpstreamSiteService {
	return &UpstreamSiteService{repo: repo, accounts: accounts, admin: admin, encryptor: encryptor, preview: os.Getenv("UPSTREAM_SITES_PREVIEW") == "true"}
}
func ProvideUpstreamSiteService(repo UpstreamSiteRepository, accounts AccountRepository, admin AdminService, encryptor UpstreamSiteSecretEncryptor, pricing *UpstreamSitePricing, settings SettingRepository, email *NotificationEmailService, users UserRepository) *UpstreamSiteService {
	s := NewUpstreamSiteService(repo, accounts, admin, encryptor)
	s.pricing = pricing
	s.balanceSettings, s.balanceMailer = settings, email
	s.balanceUsers = users
	s.Start()
	return s
}
func (s *UpstreamSiteService) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			s.runDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *UpstreamSiteService) Stop() {
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
}
func (s *UpstreamSiteService) runDue(ctx context.Context) {
	sites, err := s.repo.List(ctx)
	if err != nil {
		slog.Warn("upstream_sites_list_failed")
		return
	}
	for _, site := range sites {
		if ctx.Err() != nil {
			return
		}
		if s.balanceSettings != nil && site.Enabled && (site.Balance == nil || site.Balance.NextCheck == nil || !site.Balance.NextCheck.After(time.Now())) {
			task, cancel := context.WithTimeout(ctx, 2*time.Minute)
			_, balanceErr := s.refreshBalance(task, site.ID, true)
			cancel()
			if balanceErr != nil {
				slog.Warn("upstream_site_balance_failed", "site_id", site.ID)
			}
		}
		// Preview scans balances against the shared database, but never publishes
		// scheduled catalogue/account changes before the release is approved.
		if s.preview || !site.Enabled || (site.NextSync != nil && site.NextSync.After(time.Now())) {
			continue
		}
		task, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err = s.sync(task, site.ID, true)
		cancel()
		if err != nil {
			slog.Warn("upstream_site_sync_failed", "site_id", site.ID)
		}
	}
}
func (s *UpstreamSiteService) List(ctx context.Context) ([]UpstreamSite, error) {
	sites, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sites {
		for j := range sites[i].Bindings {
			s.refreshBindingPrice(ctx, &sites[i], &sites[i].Bindings[j])
		}
	}
	return sites, nil
}
func (s *UpstreamSiteService) saveSecret(ctx context.Context, site *UpstreamSite, credentials *SiteCredentials) error {
	raw, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	site.Secret, err = s.encryptor.Encrypt(string(raw))
	if err != nil {
		return errors.New("站点凭据加密失败")
	}
	return s.repo.Save(ctx, site)
}
func (s *UpstreamSiteService) credentials(site *UpstreamSite) (*SiteCredentials, error) {
	c := &SiteCredentials{Keys: map[string]string{}}
	if site.Secret == "" {
		return c, nil
	}
	raw, err := s.encryptor.Decrypt(site.Secret)
	if err != nil {
		return nil, errors.New("站点凭据无法解密")
	}
	if err = json.Unmarshal([]byte(raw), c); err != nil {
		return nil, errors.New("站点凭据格式无效")
	}
	return c, nil
}
func (s *UpstreamSiteService) Save(ctx context.Context, id string, input SiteInput) (*UpstreamSite, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Username = strings.TrimSpace(input.Username)
	u, err := url.Parse(input.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("请输入有效站点地址，不要包含凭据、查询参数或 API 路径")
	}
	if input.Name == "" || len(input.Name) > 120 || (input.Kind != "sub2api" && input.Kind != "newapi" && input.Kind != "kongfang") || (input.AuthMode != "password" && input.AuthMode != "token") {
		return nil, errors.New("站点名称、格式或登录方式无效")
	}
	if strings.HasSuffix(u.Path, "/v1") || strings.HasSuffix(u.Path, "/api") {
		return nil, errors.New("请填写站点首页地址，不要填写 /v1 或 /api 地址")
	}
	if math.IsNaN(input.CreditUSD) || math.IsInf(input.CreditUSD, 0) || input.CreditUSD < 0 {
		return nil, errors.New("每积分美元成本必须为非负有限数；留空或 0 时暂停价格调度")
	}
	if input.Kind == "kongfang" && u.Path != "" {
		return nil, errors.New("空凡请填写站点首页地址，不要包含 /user/balance 等路径")
	}
	fresh := id == ""
	if fresh {
		id = uuid.NewString()
	}
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site := &UpstreamSite{ID: id, Models: []SiteModel{}, Bindings: []SiteBinding{}, History: []SitePriceChange{}, Status: "pending"}
	if !fresh {
		site, err = s.repo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	credentials, err := s.credentials(site)
	if err != nil {
		return nil, err
	}
	identityChanged := !fresh && (site.BaseURL != input.BaseURL || site.Kind != input.Kind || site.Username != input.Username || site.AuthMode != input.AuthMode || site.UserID != input.UserID)
	if identityChanged && len(site.Bindings) > 0 {
		return nil, errors.New("此站点已有绑定；更换站点地址或账号身份前请先解除绑定")
	}
	authChanged := identityChanged || input.Password != "" || input.AccessToken != "" || input.RefreshToken != ""
	if identityChanged {
		site.Balance = nil
		site.ModelCatalogue = nil
		credentials = &SiteCredentials{Keys: map[string]string{}}
	}
	if input.Password != "" {
		credentials.Password = input.Password
		credentials.AccessToken = ""
		credentials.RefreshToken = ""
		credentials.Cookies = nil
	}
	if input.AccessToken != "" {
		credentials.AccessToken = strings.TrimSpace(strings.TrimPrefix(input.AccessToken, "Bearer "))
	}
	if input.RefreshToken != "" {
		credentials.RefreshToken = strings.TrimSpace(input.RefreshToken)
	}
	if input.AuthMode == "password" && (input.Username == "" || credentials.Password == "") {
		return nil, errors.New("请输入账号和密码")
	}
	if input.AuthMode == "token" && credentials.AccessToken == "" && credentials.RefreshToken == "" {
		return nil, errors.New("请至少填写一个 Access Token 或 Refresh Token")
	}
	if input.Kind == "kongfang" && input.AuthMode == "token" && credentials.AccessToken == "" {
		return nil, errors.New("空凡需要后台 Access Token，不支持仅使用 Refresh Token")
	}
	priceChanged := site.CreditUSD != input.CreditUSD
	site.CreditUSD = input.CreditUSD
	site.Name = input.Name
	site.BaseURL = input.BaseURL
	site.Kind = input.Kind
	site.AuthMode = input.AuthMode
	site.Username = input.Username
	site.UserID = input.UserID
	site.Enabled = input.Enabled
	if authChanged || priceChanged {
		if !identityChanged {
			seedSiteModelCatalogue(site)
		}
		site.LastSuccess = nil
		site.Models = []SiteModel{}
		site.NextSync = nil
		site.Status = "pending"
		site.Error = ""
	}
	if err = s.saveSecret(ctx, site, credentials); err != nil {
		return nil, err
	}
	if err = s.updatePolicies(ctx, site); err != nil {
		return nil, err
	}
	return site, nil
}
func (s *UpstreamSiteService) Sync(ctx context.Context, id string) (*UpstreamSite, error) {
	return s.sync(ctx, id, false)
}
func (s *UpstreamSiteService) sync(ctx context.Context, id string, dueOnly bool) (*UpstreamSite, error) {
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if dueOnly && (!site.Enabled || (site.NextSync != nil && site.NextSync.After(time.Now()))) {
		return site, nil
	}
	credentials, err := s.credentials(site)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	next := now.Add(5 * time.Minute)
	site.LastAttempt = &now
	site.NextSync = &next
	models, syncErr := newSiteAdapter(site, credentials).catalog(ctx)
	if syncErr != nil {
		site.Status = "error"
		site.Error = syncErr.Error()
	} else {
		old := map[string]SiteModel{}
		for _, m := range site.Models {
			old[m.GroupID+"\x00"+m.Model] = m
		}
		for _, m := range models {
			if prev, ok := old[m.GroupID+"\x00"+m.Model]; ok && !reflect.DeepEqual(prev.Tiers, m.Tiers) {
				site.History = append([]SitePriceChange{{At: now, GroupID: m.GroupID, Model: m.Model, Before: prev.Tiers, After: m.Tiers}}, site.History...)
			}
		}
		if len(site.History) > 100 {
			site.History = site.History[:100]
		}
		trackSiteModelDiscoveries(site, models, now)
		site.Models = models
		site.LastSuccess = &now
		site.Status = "connected"
		site.Error = ""
	}
	// Persist rotated authentication even when a later catalogue endpoint fails.
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelPersist()
	if err = s.saveSecret(persistCtx, site, credentials); err != nil {
		return nil, err
	}
	if err = s.updatePolicies(persistCtx, site); err != nil {
		return nil, err
	}
	return site, nil
}
func findSiteModel(site *UpstreamSite, groupID, model string) *SiteModel {
	for i := range site.Models {
		if site.Models[i].GroupID == groupID && site.Models[i].Model == model {
			return &site.Models[i]
		}
	}
	return nil
}
func (s *UpstreamSiteService) Bind(ctx context.Context, id string, input SiteBinding) (*UpstreamSite, error) {
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// Existing provisioned bindings remain editable even when their catalogue
	// disappeared or expired. Pausing must never depend on upstream access.
	if input.ID != "" {
		for i := range site.Bindings {
			b := &site.Bindings[i]
			if b.ID != input.ID || b.AccountID == 0 {
				continue
			}
			if b.GroupID != input.GroupID || b.Model != input.Model || b.LocalGroupID != input.LocalGroupID || b.LocalModel != input.LocalModel {
				return nil, errors.New("更换模型请新增绑定")
			}
			b.Limits = siteMergeTierSwitches(b.Limits, input.Limits)
			b.Enabled = input.Enabled
			s.refreshBindingPrice(ctx, site, b)
			if err = s.repo.Save(ctx, site); err != nil {
				return nil, err
			}
			if err = s.updatePolicies(ctx, site); err != nil {
				return nil, err
			}
			return site, nil
		}
	}
	input.LocalModel = strings.TrimSpace(input.LocalModel)
	if input.LocalModel == "" || len(input.LocalModel) > 200 || strings.ContainsAny(input.LocalModel, "*?\r\n") {
		return nil, errors.New("请输入明确的本地模型名称，不支持通配符")
	}
	group, err := s.admin.GetGroup(ctx, input.LocalGroupID)
	if err != nil {
		return nil, err
	}
	if group.Platform != PlatformOpenAI && group.Platform != PlatformAnthropic && group.Platform != PlatformGemini {
		return nil, errors.New("首版支持绑定到 OpenAI、Anthropic、Gemini 分组")
	}
	if err = s.admin.ValidateAccountGroupBindings(ctx, []int64{group.ID}); err != nil {
		return nil, err
	}
	model := findSiteModel(site, input.GroupID, input.Model)
	if model == nil {
		return nil, errors.New("模型不在同步目录中，请先同步站点")
	}
	if site.LastSuccess == nil || time.Since(*site.LastSuccess) > 10*time.Minute {
		return nil, errors.New("价格目录已过期，请先同步")
	}
	if model.Platform != "" && (site.Kind == "sub2api" || site.Kind == "kongfang") && model.Platform != group.Platform {
		return nil, errors.New("本地分组与上游模型的平台不匹配")
	}
	s.refreshBindingPrice(ctx, site, &input)
	var binding *SiteBinding
	if input.ID != "" {
		for i := range site.Bindings {
			if site.Bindings[i].ID == input.ID {
				binding = &site.Bindings[i]
				break
			}
		}
		if binding == nil {
			return nil, errors.New("绑定不存在")
		}
		if binding.GroupID != input.GroupID || binding.Model != input.Model || binding.LocalGroupID != input.LocalGroupID || binding.LocalModel != input.LocalModel {
			return nil, errors.New("已有绑定只能修改档位启停；更换模型请新增绑定")
		}
		binding.Limits = siteMergeTierSwitches(binding.Limits, input.Limits)
		binding.Enabled = input.Enabled
	} else {
		for i := range site.Bindings {
			b := &site.Bindings[i]
			if b.GroupID == input.GroupID && b.Model == input.Model && b.LocalGroupID == input.LocalGroupID && b.LocalModel == input.LocalModel {
				binding = b
				break
			}
		}
		if binding == nil {
			input.ID = uuid.NewString()
			input.AccountID = 0
			input.Platform = group.Platform
			input.Status = "provisioning"
			input.Error = ""
			site.Bindings = append(site.Bindings, input)
			binding = &site.Bindings[len(site.Bindings)-1]
		}
	}
	// Durable operation identity precedes any remote side effect.
	if err = s.repo.Save(ctx, site); err != nil {
		return nil, err
	}
	credentials, err := s.credentials(site)
	if err != nil {
		return nil, err
	}
	key, keyErr := newSiteAdapter(site, credentials).ensureKey(ctx, binding)
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelPersist()
	if keyErr != nil {
		binding.Status = "error"
		binding.Error = keyErr.Error()
		if err = s.saveSecret(persistCtx, site, credentials); err != nil {
			return nil, err
		}
		return site, nil
	}
	if err = s.saveSecret(persistCtx, site, credentials); err != nil {
		return nil, err
	}
	if binding.AccountID == 0 {
		recovered, err := s.accounts.FindByExtraField(ctx, "upstream_site_binding_id", binding.ID)
		if err != nil {
			return nil, err
		}
		if len(recovered) > 0 {
			binding.AccountID = recovered[0].ID
		} else {
			policy := BuildSiteAccountPolicy(site, binding)
			raw, _ := json.Marshal(policy)
			var policyMap map[string]any
			_ = json.Unmarshal(raw, &policyMap)
			account := &Account{Name: siteManagedAccountName(site.Name, binding.Model, model.GroupName), Platform: group.Platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 50, Credentials: map[string]any{"base_url": site.BaseURL, "api_key": key, "model_mapping": map[string]any{binding.LocalModel: binding.Model}, SiteBindingCredentialKey: binding.ID}, Extra: map[string]any{"upstream_site_binding_id": binding.ID, "upstream_site_id": site.ID, SitePolicyExtraKey: policyMap, UpstreamBillingProbeEnabledExtraKey: false}}
			if s.preview {
				account.Status = StatusDisabled
				account.Extra["upstream_site_preview_pending"] = true
			}
			if err = s.accounts.Create(ctx, account); err != nil {
				return nil, err
			}
			binding.AccountID = account.ID
		}
	}
	if err = s.accounts.BindGroups(ctx, binding.AccountID, []int64{binding.LocalGroupID}); err != nil {
		return nil, err
	}
	binding.Error = ""
	if err = s.repo.Save(ctx, site); err != nil {
		return nil, err
	}
	if err = s.updatePolicies(ctx, site); err != nil {
		return nil, err
	}
	return site, nil
}
func siteManagedAccountName(siteName, model, upstreamGroup string) string {
	return truncateUTF8(fmt.Sprintf("【%s】%s（%s）", siteName, model, upstreamGroup), 100)
}

func BuildSiteAccountPolicy(site *UpstreamSite, b *SiteBinding) SiteAccountPolicy {
	p := SiteAccountPolicy{SiteKind: site.Kind, LocalGroupID: b.LocalGroupID, SiteID: site.ID, SiteName: site.Name, BindingID: b.ID, LocalModel: b.LocalModel, UpstreamModel: b.Model, Enabled: site.Enabled && b.Enabled, Limits: b.Limits, Tiers: []SitePriceTier{}}
	if site.LastSuccess != nil {
		p.FreshUntil = site.LastSuccess.Add(10 * time.Minute)
	}
	if m := findSiteModel(site, b.GroupID, b.Model); m != nil {
		p.Image = m.Image
		p.Tiers = siteComparisonTiers(m.Image, m.Tiers)
		p.Reason = m.Reason
	} else {
		p.Reason = "上游模型或分组已不可用"
	}
	return p
}
func (s *UpstreamSiteService) updatePolicies(ctx context.Context, site *UpstreamSite) error {
	for i := range site.Bindings {
		b := &site.Bindings[i]
		s.refreshBindingPrice(ctx, site, b)
		if b.AccountID == 0 {
			continue
		}
		policy := BuildSiteAccountPolicy(site, b)
		raw, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		var value map[string]any
		if err = json.Unmarshal(raw, &value); err != nil {
			return err
		}
		if err = s.accounts.UpdateExtra(ctx, b.AccountID, map[string]any{SitePolicyExtraKey: value}); err != nil {
			if errors.Is(err, ErrAccountNotFound) {
				b.Status = "blocked"
				b.Error = "托管账号已被删除，请解除绑定后重新绑定"
				continue
			}
			return err
		}
		allowed := 0
		for _, tier := range policy.Tiers {
			if siteTierReason(policy, tier.Key, time.Now()) == "" {
				allowed++
			}
		}
		b.Status = "blocked"
		if allowed > 0 {
			b.Status = "partial"
			if allowed == len(policy.Tiers) {
				b.Status = "ready"
			}
		}
		account, err := s.accounts.GetByID(ctx, b.AccountID)
		if err != nil {
			return err
		}
		nameChanged := false
		if model := findSiteModel(site, b.GroupID, b.Model); model != nil {
			name := siteManagedAccountName(site.Name, b.Model, model.GroupName)
			nameChanged = account.Name != name
			account.Name = name
		}
		if pending, _ := account.Extra["upstream_site_preview_pending"].(bool); pending {
			if s.preview {
				b.Status = "preview"
			} else {
				account.Status = StatusActive
				account.Extra["upstream_site_preview_pending"] = false
				nameChanged = true
			}
		}
		if nameChanged {
			if err = s.accounts.Update(ctx, account); err != nil {
				return err
			}
		}
	}
	return s.repo.Save(ctx, site)
}
func (s *UpstreamSiteService) Unbind(ctx context.Context, id, bindingID string) (*UpstreamSite, error) {
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	for i, b := range site.Bindings {
		if b.ID == bindingID {
			if b.AccountID > 0 {
				if err = s.accounts.Delete(ctx, b.AccountID); err != nil && !errors.Is(err, ErrAccountNotFound) {
					return nil, err
				}
			}
			site.Bindings = append(site.Bindings[:i], site.Bindings[i+1:]...)
			return site, s.repo.Save(ctx, site)
		}
	}
	return nil, errors.New("绑定不存在")
}
func (s *UpstreamSiteService) Delete(ctx context.Context, id string) error {
	unlock, err := s.repo.Lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if len(site.Bindings) > 0 {
		return errors.New("请先解除该站点的模型绑定")
	}
	return s.repo.Delete(ctx, id)
}
