package querybroker

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

// PostgreSQL uses bounded caller-owned database/sql pools. It does not run
// migrations, hold a transaction across OAuth, or rely on replica-local locks.
type PostgreSQL struct {
	DB    *sql.DB
	Vault *Vault
}

func (s *PostgreSQL) Ready(ctx context.Context) error {
	var version int
	var fingerprint string
	if s == nil || s.DB == nil || s.Vault == nil {
		return ErrUnavailable
	}
	if s.DB.QueryRowContext(ctx, "SELECT v.version,s.key_fingerprint FROM hhc_query.schema_version v CROSS JOIN hhc_query.broker_settings s WHERE v.version=1 AND s.singleton").Scan(&version, &fingerprint) != nil {
		return ErrUnavailable
	}
	if fingerprint != s.Vault.Fingerprint() {
		return ErrUnavailable
	}
	return nil
}
func (s *PostgreSQL) Policy(ctx context.Context, id string) (Policy, error) {
	return loadPolicy(ctx, s.DB, id)
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadPolicy(ctx context.Context, q rowQuery, id string) (Policy, error) {
	var p Policy
	err := q.QueryRowContext(ctx, `SELECT caller_id,revision,enabled,hhc_mode,hhc_grant,hhc_quota,youtube_mode,youtube_grant,youtube_quota,channel_id,requests_per_minute FROM hhc_query.caller_policy WHERE caller_id=$1`, id).Scan(&p.CallerID, &p.Revision, &p.Enabled, &p.HHCMode, &p.HHCGrant, &p.HHCQuota, &p.YouTubeMode, &p.YouTubeGrant, &p.YouTubeQuota, &p.ChannelID, &p.RequestsPerMinute)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !p.Enabled {
		return p, ErrDenied
	}
	if err != nil {
		return p, ErrUnavailable
	}
	return p, nil
}
func loadPolicyLocked(ctx context.Context, tx *sql.Tx, id string) (Policy, error) {
	var p Policy
	err := tx.QueryRowContext(ctx, `SELECT caller_id,revision,enabled,hhc_mode,hhc_grant,hhc_quota,youtube_mode,youtube_grant,youtube_quota,channel_id,requests_per_minute FROM hhc_query.lock_policy($1)`, id).Scan(&p.CallerID, &p.Revision, &p.Enabled, &p.HHCMode, &p.HHCGrant, &p.HHCQuota, &p.YouTubeMode, &p.YouTubeGrant, &p.YouTubeQuota, &p.ChannelID, &p.RequestsPerMinute)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !p.Enabled {
		return p, ErrDenied
	}
	if err != nil {
		return p, ErrUnavailable
	}
	return p, nil
}
func resource(p Policy, source, res string) (grant, quota string, err error) {
	switch source {
	case "hhc":
		if res != "content:videos" || p.HHCMode != "public" && p.HHCMode != "human" {
			return "", "", ErrDenied
		}
		return p.HHCGrant, p.HHCQuota, nil
	case "youtube":
		if res != p.ChannelID || !youtube.ValidChannel(res) || p.YouTubeMode != "public" && p.YouTubeMode != "owner" {
			return "", "", ErrDenied
		}
		return p.YouTubeGrant, p.YouTubeQuota, nil
	}
	return "", "", ErrDenied
}
func (s *PostgreSQL) Permit(ctx context.Context, p Policy, source, res, bucket string, cost int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	current, err := loadPolicyLocked(ctx, tx, p.CallerID)
	if err != nil {
		return callerFailure(err)
	}
	if current.Revision != p.Revision {
		return callerFailure(ErrDenied)
	}
	grantID, quotaID := "", ""
	if source != "" {
		grantID, quotaID, err = resource(current, source, res)
		if err != nil {
			return err
		}
		if grantID != "" {
			g, _, err := loadGrantLocked(ctx, tx, grantID)
			if err != nil {
				return err
			}
			if err = validateGrant(current, g, source, res); err != nil {
				return err
			}
			if g.State != "active" || !g.ExpiresAt.After(time.Now()) {
				return ErrReauthorize
			}
		}
	}
	if bucket == "authorization" && cost == 0 {
		return safeCommit(tx)
	}
	if cost != 1 {
		return ErrDenied
	}
	callerBucket := bucket
	if source == "" {
		if bucket != "mcp.requests" {
			return ErrDenied
		}
	} else {
		if source == "hhc" && bucket != "hhc.read" || source == "youtube" && bucket != "youtube.search" && bucket != "youtube.default" {
			return ErrDenied
		}
		var seconds int
		var maximum int64
		var zone string
		if err = tx.QueryRowContext(ctx, `SELECT period_seconds,maximum,reset_zone FROM hhc_query.quota_limit WHERE quota_id=$1 AND bucket=$2`, quotaID, bucket).Scan(&seconds, &maximum, &zone); errors.Is(err, sql.ErrNoRows) {
			return ErrDenied
		} else if err != nil {
			return ErrUnavailable
		}
		if err = spend(ctx, tx, "project:"+quotaID, bucket, seconds, maximum, zone); err != nil {
			return err
		}
		callerBucket = "upstream.requests"
	}
	if err = spend(ctx, tx, "caller:"+p.CallerID, callerBucket, 60, int64(current.RequestsPerMinute), "UTC"); err != nil {
		return err
	}
	return safeCommit(tx)
}

// Provider daily windows use its calendar, including Pacific DST. All window
// timestamps come from PostgreSQL, not clocks on independently serving replicas.
const quotaWindowCTE = `WITH moment AS (SELECT coalesce($5::timestamptz,clock_timestamp()) AS at), w AS (
 SELECT CASE WHEN $3::bigint=86400 THEN extract(epoch FROM (date_trunc('day',at AT TIME ZONE $6) AT TIME ZONE $6))::bigint
 ELSE (floor(extract(epoch FROM at))::bigint/$3::bigint)*$3::bigint END AS start,
 CASE WHEN $3::bigint=86400 THEN (date_trunc('day',at AT TIME ZONE $6)+interval '1 day') AT TIME ZONE $6
 ELSE to_timestamp((floor(extract(epoch FROM at))::bigint/$3::bigint)*$3::bigint)+($3::bigint * interval '1 second') END AS expires FROM moment)
`

func spend(ctx context.Context, tx *sql.Tx, key, bucket string, seconds int, maximum int64, zone string) error {
	return spendAt(ctx, tx, key, bucket, seconds, maximum, zone, nil)
}
func spendAt(ctx context.Context, tx *sql.Tx, key, bucket string, seconds int, maximum int64, zone string, at *time.Time) error {
	var used int64
	err := tx.QueryRowContext(ctx, quotaWindowCTE+`INSERT INTO hhc_query.quota_usage(quota_key,bucket,window_start,used,expires_at)
 SELECT $1,$2,start,1,expires FROM w
 ON CONFLICT(quota_key,bucket,window_start) DO UPDATE SET used=hhc_query.quota_usage.used+1
 WHERE hhc_query.quota_usage.used<$4 RETURNING used`, key, bucket, seconds, maximum, at, zone).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrQuota
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
func safeCommit(tx *sql.Tx) error {
	if tx.Commit() != nil {
		return ErrUnavailable
	}
	return nil
}
func loadGrant(ctx context.Context, q rowQuery, id string) (Grant, []byte, error) {
	var g Grant
	var encrypted []byte
	err := q.QueryRowContext(ctx, `SELECT grant_id,owner_id,source,resource,kind,scope,generation,state,expires_at,ciphertext FROM hhc_query.grant_state WHERE grant_id=$1`, id).Scan(&g.ID, &g.Owner, &g.Source, &g.Resource, &g.Kind, &g.Scope, &g.Generation, &g.State, &g.ExpiresAt, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil, ErrDenied
	}
	if err != nil {
		return g, nil, ErrUnavailable
	}
	return g, encrypted, nil
}
func loadGrantLocked(ctx context.Context, tx *sql.Tx, id string) (Grant, []byte, error) {
	var g Grant
	var encrypted []byte
	err := tx.QueryRowContext(ctx, `SELECT grant_id,owner_id,source,resource,kind,scope,generation,state,expires_at,ciphertext FROM hhc_query.grant_state WHERE grant_id=$1 FOR SHARE`, id).Scan(&g.ID, &g.Owner, &g.Source, &g.Resource, &g.Kind, &g.Scope, &g.Generation, &g.State, &g.ExpiresAt, &encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return g, nil, ErrDenied
	}
	if err != nil {
		return g, nil, ErrUnavailable
	}
	return g, encrypted, nil
}
func validateGrant(p Policy, g Grant, source, res string) error {
	if g.Source != source || g.Resource != res || g.State == "revoked" {
		return ErrDenied
	}
	if source == "hhc" {
		if p.HHCMode != "human" || g.Kind != "human" || g.Owner != p.CallerID || g.Scope != "cms:pages:read" {
			return ErrDenied
		}
	}
	if source == "youtube" {
		if p.YouTubeMode == "public" {
			if g.Kind != "api_key" || g.Scope != "" || g.Owner != "project:"+p.YouTubeQuota {
				return ErrDenied
			}
		} else if g.Kind != "oauth" || g.Owner != p.CallerID || g.Scope != youtube.ReadScope {
			return ErrDenied
		}
	}
	return nil
}
func (s *PostgreSQL) Credential(ctx context.Context, p Policy, source, res string, refresh Refresher) (Grant, error) {
	id, _, err := resource(p, source, res)
	if err != nil || id == "" {
		return Grant{}, ErrDenied
	}
	backoff := 50 * time.Millisecond
	for {
		current, err := s.Policy(ctx, p.CallerID)
		if err != nil {
			return Grant{}, callerFailure(err)
		}
		if current.Revision != p.Revision {
			return Grant{}, callerFailure(ErrDenied)
		}
		g, encrypted, err := loadGrant(ctx, s.DB, id)
		if err != nil {
			return g, err
		}
		if err = validateGrant(current, g, source, res); err != nil {
			return Grant{}, err
		}
		if g.State == "uncertain" {
			return Grant{}, ErrReauthorize
		}
		if g.State == "rotating" {
			timer := time.NewTimer(backoff + time.Duration(rand.Int64N(int64(backoff/4)+1)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return Grant{}, ErrReauthorize
			case <-timer.C:
			}
			backoff = min(2*backoff, 500*time.Millisecond)
			continue
		}
		if g.State != "active" {
			return Grant{}, ErrDenied
		}
		if err = s.Vault.open(&g, encrypted); err != nil {
			return Grant{}, err
		}
		if g.Kind == "api_key" {
			if time.Until(g.ExpiresAt) < 60*time.Second {
				return Grant{}, ErrReauthorize
			}
			g.ExpiresAt = minTime(g.ExpiresAt, time.Now().Add(2*time.Minute))
			return g, nil
		}
		if time.Until(g.ExpiresAt) > 60*time.Second {
			return g, nil
		}
		if refresh == nil || g.Secrets.RefreshToken == "" {
			return Grant{}, ErrReauthorize
		}
		// Commit a durable generation fence BEFORE any refresh exchange. A dead
		// owner leaves rotating/uncertain forever until explicit reauthorization;
		// wall-clock lease expiry never permits replay of the consumed refresh token.
		var registeredOwner string
		if err = s.DB.QueryRowContext(ctx, `SELECT grant_id FROM hhc_query.refresh_identity WHERE fingerprint=$1`, s.Vault.refreshIdentity(g)).Scan(&registeredOwner); err != nil || registeredOwner != g.ID {
			return Grant{}, ErrReauthorize
		}
		rotated := g
		rotated.Generation++
		rotated.State = "rotating"
		sealed, err := s.Vault.seal(rotated)
		if err != nil {
			return Grant{}, err
		}
		result, err := s.DB.ExecContext(ctx, `UPDATE hhc_query.grant_state SET state='rotating',generation=$2,ciphertext=$3,rotation_started_at=clock_timestamp(),updated_at=clock_timestamp() WHERE grant_id=$1 AND generation=$4 AND state='active'`, g.ID, rotated.Generation, sealed, g.Generation)
		if err != nil {
			return Grant{}, ErrUnavailable
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Grant{}, ErrUnavailable
		}
		if n == 0 {
			continue
		}
		next, expires, exchangeErr := refresh.Refresh(ctx, rotated)
		if exchangeErr != nil || next.AccessToken == "" || !validSecrets(next) || time.Until(expires) < 60*time.Second {
			s.uncertain(g.ID, rotated.Generation)
			return Grant{}, ErrReauthorize
		}
		// A provider that does not rotate its refresh token may retain the existing
		// one; every uncertain exchange remains fenced regardless of provider policy.
		if next.ClientID == "" {
			next.ClientID = g.Secrets.ClientID
		}
		if next.ClientSecret == "" {
			next.ClientSecret = g.Secrets.ClientSecret
		}
		if next.ClientID != g.Secrets.ClientID || next.ClientSecret != g.Secrets.ClientSecret {
			s.uncertain(g.ID, rotated.Generation)
			return Grant{}, ErrReauthorize
		}
		if next.RefreshToken == "" {
			next.RefreshToken = g.Secrets.RefreshToken
		}
		rotated.Secrets = next
		expires = expires.UTC().Truncate(time.Microsecond)
		rotated.ExpiresAt = expires
		rotated.State = "active"
		sealed, err = s.Vault.seal(rotated)
		if err != nil {
			s.uncertain(g.ID, rotated.Generation)
			return Grant{}, err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return Grant{}, ErrUnavailable
		}
		if err = s.reserveRefresh(ctx, tx, rotated, rotated.Secrets.RefreshToken == g.Secrets.RefreshToken); err != nil {
			tx.Rollback()
			s.uncertain(g.ID, rotated.Generation)
			if errors.Is(err, ErrDenied) {
				return Grant{}, ErrReauthorize
			}
			return Grant{}, err
		}
		result, err = tx.ExecContext(ctx, `UPDATE hhc_query.grant_state SET state='active',expires_at=$3,ciphertext=$4,updated_at=clock_timestamp() WHERE grant_id=$1 AND generation=$2 AND state='rotating'`, g.ID, rotated.Generation, expires, sealed)
		if err != nil {
			tx.Rollback()
			return Grant{}, ErrUnavailable
		}
		n, err = result.RowsAffected()
		if err != nil || n != 1 {
			tx.Rollback()
			return Grant{}, ErrReauthorize
		}
		if err = safeCommit(tx); err != nil {
			return Grant{}, err
		}

		// Re-read policy after refresh; the runtime also performs a final read fence.
		latest, err := s.Policy(ctx, p.CallerID)
		if err != nil || latest.Revision != p.Revision {
			return Grant{}, callerFailure(ErrDenied)
		}
		return rotated, nil
	}
}
func (s *PostgreSQL) uncertain(id string, generation int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.DB.ExecContext(ctx, `UPDATE hhc_query.grant_state SET state='uncertain',updated_at=clock_timestamp() WHERE grant_id=$1 AND generation=$2 AND state='rotating'`, id, generation)
}

// ImportGrant is for an explicitly authorized consent/provisioning owner, never
// an MCP tool. Runtime roles cannot INSERT. No production caller invokes it here.
func (s *PostgreSQL) ImportGrant(ctx context.Context, g Grant) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	if ValidateImport(g) != nil {
		return ErrDenied
	}
	g.ExpiresAt = g.ExpiresAt.UTC().Truncate(time.Microsecond)
	raw, err := s.Vault.seal(g)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err = s.reserveRefresh(ctx, tx, g, false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_query.grant_state(grant_id,owner_id,source,resource,kind,scope,generation,state,expires_at,ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, g.ID, g.Owner, g.Source, g.Resource, g.Kind, g.Scope, g.Generation, g.State, g.ExpiresAt, raw)
	if err != nil {
		return ErrUnavailable
	}
	return safeCommit(tx)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *PostgreSQL) reserveRefresh(ctx context.Context, tx *sql.Tx, g Grant, allowExisting bool) error {
	if g.Secrets.RefreshToken == "" {
		return nil
	}
	fingerprint := s.Vault.refreshIdentity(g)
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_query.refresh_identity(fingerprint,grant_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, fingerprint, g.ID)
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if n == 1 {
		return nil
	}
	if !allowExisting {
		return ErrDenied
	}
	var owner string
	if tx.QueryRowContext(ctx, `SELECT grant_id FROM hhc_query.refresh_identity WHERE fingerprint=$1`, fingerprint).Scan(&owner) != nil {
		return ErrUnavailable
	}
	if owner != g.ID {
		return ErrDenied
	}
	return nil
}
