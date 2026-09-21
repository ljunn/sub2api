package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type upstreamSiteRepository struct{ db *sql.DB }

func NewUpstreamSiteRepository(db *sql.DB) service.UpstreamSiteRepository {
	return &upstreamSiteRepository{db: db}
}

func (r *upstreamSiteRepository) List(ctx context.Context) ([]service.UpstreamSite, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT document FROM upstream_sites ORDER BY document->>'name', id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.UpstreamSite{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var site service.UpstreamSite
		if err = json.Unmarshal(raw, &site); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}
func (r *upstreamSiteRepository) Get(ctx context.Context, id string) (*service.UpstreamSite, error) {
	var raw []byte
	var secret string
	if err := r.db.QueryRowContext(ctx, "SELECT document, secret FROM upstream_sites WHERE id=$1", id).Scan(&raw, &secret); err != nil {
		return nil, err
	}
	var site service.UpstreamSite
	if err := json.Unmarshal(raw, &site); err != nil {
		return nil, err
	}
	site.Secret = secret
	return &site, nil
}

// Publish the catalogue, every bound-account policy and scheduler invalidation
// events in one transaction. A failed write can never publish only half a price change.
func (r *upstreamSiteRepository) Save(ctx context.Context, site *service.UpstreamSite) error {
	raw, err := json.Marshal(site)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO upstream_sites(id,document,secret) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET document=EXCLUDED.document,secret=EXCLUDED.secret,updated_at=NOW()", site.ID, raw, site.Secret)
	if err != nil {
		return err
	}
	for _, binding := range site.Bindings {
		if binding.AccountID <= 0 {
			continue
		}
		policy := service.BuildSiteAccountPolicy(site, &binding)
		payload, err := json.Marshal(map[string]any{service.SitePolicyExtraKey: policy})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb) || $1::jsonb, updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL AND credentials->>'upstream_site_binding_id'=$3", string(payload), binding.AccountID, binding.ID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count > 0 {
			if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &binding.AccountID, nil, nil); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (r *upstreamSiteRepository) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM upstream_sites WHERE id=$1", id)
	return err
}

// Session locks serialize token rotation and provisioning across production and
// preview even though those processes intentionally use different Redis DBs.
func (r *upstreamSiteRepository) Lock(ctx context.Context, id string) (func(), error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtextextended($1,0))", "upstream-site:"+id)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(c, "SELECT pg_advisory_unlock(hashtextextended($1,0))", "upstream-site:"+id); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, nil
}
