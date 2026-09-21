package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSiteBalanceUSDConversionAndChineseAlerts(t *testing.T) {
	for _, tc := range []struct {
		name, currency         string
		amount, rate, expected float64
		alert                  bool
	}{
		{"credits below", "积分", 1900, 100, 19, true},
		{"credits exact", "积分", 2000, 100, 20, false},
		{"CNY below", "CNY", 190, 10, 19, true},
		{"CNY exact", "CNY", 200, 10, 20, false},
		{"USD default", "USD", 20, 0, 20, false},
		{"one to one", "积分", 19, 1, 19, true},
		{"zero", "CNY", 0, 10, 0, true},
		{"negative", "CNY", -100, 10, -10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := siteTestService()
			mailer := &siteBalanceTestMailer{}
			svc.balanceSettings, svc.balanceMailer = balanceTestSettings(), mailer
			site := &UpstreamSite{ID: "converted", Name: "站点", BalanceUnitsPerUSD: tc.rate, Balance: &SiteBalance{Amount: &tc.amount, Currency: tc.currency}}
			require.NoError(t, svc.notifySiteBalance(context.Background(), site, time.Now()))
			require.NotNil(t, site.Balance.AmountUSD)
			assert.InDelta(t, tc.expected, *site.Balance.AmountUSD, 1e-9)
			assert.Equal(t, tc.amount, *site.Balance.Amount)
			assert.Equal(t, tc.alert, mailer.calls == 1)
			if tc.alert {
				assert.Equal(t, "zh", mailer.input.Locale)
				assert.Equal(t, "USD", mailer.input.Variables["currency"])
				assert.Equal(t, balanceAmount(tc.expected), mailer.input.Variables["current_balance"])
			}
		})
	}
}

func TestSiteBalanceConversionDefaultsAndUnknown(t *testing.T) {
	amount := 100.0
	site := &UpstreamSite{Kind: "newapi", Balance: &SiteBalance{Amount: &amount, Currency: "CNY", NativeUnitsPerUSD: 5}}
	convertSiteBalanceUSD(site)
	require.Equal(t, 20.0, *site.Balance.AmountUSD)
	site.BalanceUnitsPerUSD = 10
	convertSiteBalanceUSD(site)
	require.Equal(t, 10.0, *site.Balance.AmountUSD, "manual rate overrides upstream display rate")
	site.BalanceUnitsPerUSD, site.Balance.NativeUnitsPerUSD = 0, 0
	convertSiteBalanceUSD(site)
	require.Nil(t, site.Balance.AmountUSD)
	require.NotEmpty(t, site.Balance.ConversionError)
	site.Kind, site.CreditUSD, site.Balance.Currency = "kongfang", .01, "积分"
	convertSiteBalanceUSD(site)
	require.Equal(t, 1.0, *site.Balance.AmountUSD, "reuse existing Kongfang cost configuration")
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		site.BalanceUnitsPerUSD = invalid
		convertSiteBalanceUSD(site)
		require.Nil(t, site.Balance.AmountUSD)
	}
}

func TestSiteBalanceSavedRateRecalculatesAndSchedulesCheck(t *testing.T) {
	svc, repo, _ := siteTestService()
	rate := 100.0
	input := SiteInput{Name: "空凡", BaseURL: "https://example.test", Kind: "kongfang", AuthMode: "token", AccessToken: "token", Enabled: true, BalanceUnitsPerUSD: &rate}
	site, err := svc.Save(context.Background(), "", input)
	require.NoError(t, err)
	require.Equal(t, .01, site.CreditUSD)
	amount, future := 1900.0, time.Now().Add(time.Hour)
	repo.sites[site.ID].Balance = &SiteBalance{Amount: &amount, Currency: "积分", NextCheck: &future, NextNotify: &future, NotifyCycle: "old-cycle"}
	rate = 10
	site, err = svc.Save(context.Background(), site.ID, input)
	require.NoError(t, err)
	require.Equal(t, .1, site.CreditUSD)
	require.Equal(t, 190.0, *site.Balance.AmountUSD)
	require.Nil(t, site.Balance.NextCheck)
	require.Nil(t, site.Balance.NextNotify)
	require.Empty(t, site.Balance.NotifyCycle)
	for _, invalid := range []float64{-1, math.Inf(1), math.NaN(), 1e13, 1e-13} {
		input.BalanceUnitsPerUSD = &invalid
		_, err := svc.Save(context.Background(), site.ID, input)
		require.Error(t, err)
	}
}

func TestSiteBalanceChineseMailIncludesChineseFooterAndUSD(t *testing.T) {
	notifications := NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil)
	preview, err := notifications.PreviewTemplate(context.Background(), NotificationEmailPreviewInput{Event: NotificationEmailEventUpstreamSiteBalanceLow, Locale: "zh"})
	require.NoError(t, err)
	assert.Contains(t, preview.Subject, "站点余额不足")
	assert.Contains(t, preview.HTML, "提醒阈值")
	assert.Contains(t, preview.HTML, "USD")
	assert.Contains(t, preview.HTML, "请勿直接回复")
	assert.NotContains(t, preview.HTML, "This email was sent")
	assert.NotContains(t, preview.HTML, "Current balance")
}
