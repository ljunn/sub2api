package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpsPreviewRetainsErrorAPIsAndLoggingWithoutBackgroundTasks(t *testing.T) {
	cfg := &config.Config{Ops: config.OpsConfig{Enabled: true, DisableBackgroundTasks: true}}
	ctx := context.Background()
	recorded := 0
	repo := &opsRepoMock{
		InsertErrorLogFn: func(context.Context, *OpsInsertErrorLogInput) (int64, error) {
			recorded++
			return 42, nil
		},
		GetErrorLogByIDFn: func(context.Context, int64) (*OpsErrorLogDetail, error) {
			return &OpsErrorLogDetail{}, nil
		},
	}
	svc := NewOpsService(repo, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	require.True(t, svc.IsMonitoringEnabled(ctx))
	require.NoError(t, svc.RecordError(ctx, &OpsInsertErrorLogInput{ErrorMessage: "upstream unavailable"}))
	require.Equal(t, 1, recorded)
	_, err := svc.GetErrorLogs(ctx, &OpsErrorLogFilter{})
	require.NoError(t, err)
	_, err = svc.GetErrorLogByID(ctx, 42)
	require.NoError(t, err)

	// Deliberately unusable dependencies make any accidental scheduled execution
	// fail. A deployment override must take precedence over shared DB settings.
	collector := &OpsMetricsCollector{cfg: cfg, opsRepo: repo, db: &sql.DB{}}
	collector.Start()
	require.Nil(t, collector.stopCh)
	collector.collectOnce()
	collector.Stop()

	aggregation := &OpsAggregationService{cfg: cfg, opsRepo: repo, db: &sql.DB{}}
	aggregation.Start()
	require.Nil(t, aggregation.stopCh)
	aggregation.aggregateHourly()
	aggregation.aggregateDaily()
	aggregation.Stop()

	alerts := &OpsAlertEvaluatorService{cfg: cfg, opsRepo: repo, opsService: svc}
	alerts.Start()
	require.Nil(t, alerts.stopCh)
	alerts.evaluateOnce(time.Minute)
	alerts.Stop()

	reports := &OpsScheduledReportService{cfg: cfg, opsService: svc, emailService: &EmailService{}}
	reports.Start()
	require.Nil(t, reports.stopCtx)
	reports.runOnce()
	reports.Stop()

	cleanup := &OpsCleanupService{cfg: cfg, opsRepo: repo, db: &sql.DB{}, effective: opsCleanupEffectiveConfig{OpsCleanupConfig: config.OpsCleanupConfig{Enabled: true}}}
	cleanup.Start()
	require.False(t, cleanup.started)
	require.NoError(t, cleanup.Reload(ctx))
	require.NoError(t, cleanup.applyScheduleLocked(ctx))
	require.Nil(t, cleanup.cron)
	cleanup.runScheduled()
	cleanup.Stop()
}
