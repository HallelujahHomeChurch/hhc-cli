package querybroker

import (
	"context"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
)

var ownerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func hashedCaller(id string) bool {
	raw, err := hex.DecodeString(id)
	return err == nil && len(raw) == 32 && strings.ToLower(id) == id
}
func ValidateImport(g Grant) error {
	if !ownerID.MatchString(g.ID) || g.Generation != 1 || g.State != "active" || (g.ExpiresAt.Year() < 2000 || g.ExpiresAt.Year() > 2200 || time.Until(g.ExpiresAt) < 60*time.Second && (g.Kind != "oauth" || g.ExpiresAt.Before(time.Now().Add(-24*time.Hour)))) || g.Secrets.AccessToken == "" || len(g.Secrets.AccessToken) > 8192 || len(g.Secrets.RefreshToken) > 8192 || len(g.Secrets.ClientID) > 512 || len(g.Secrets.ClientSecret) > 4096 || !validSecrets(g.Secrets) {
		return ErrDenied
	}
	switch {
	case g.Source == "hhc" && g.Kind == "human":
		if !hashedCaller(g.Owner) || g.Resource != "content:videos" || g.Scope != "cms:pages:read" || g.Secrets.RefreshToken != "" || g.Secrets.ClientID != "" || g.Secrets.ClientSecret != "" {
			return ErrDenied
		}
	case g.Source == "youtube" && g.Kind == "api_key":
		if !strings.HasPrefix(g.Owner, "project:") || !ownerID.MatchString(strings.TrimPrefix(g.Owner, "project:")) || !youtube.ValidChannel(g.Resource) || g.Scope != "" || g.Secrets.RefreshToken != "" || g.Secrets.ClientID != "" || g.Secrets.ClientSecret != "" {
			return ErrDenied
		}
	case g.Source == "youtube" && g.Kind == "oauth":
		if !hashedCaller(g.Owner) || !youtube.ValidChannel(g.Resource) || g.Scope != youtube.ReadScope || g.Secrets.RefreshToken == "" || g.Secrets.ClientID == "" {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	return nil
}

// Owner methods are never exposed by MCP or invoked on service startup. They
// require a separately approved database owner role and explicit operator intent.
func (s *PostgreSQL) BindVault(ctx context.Context) error {
	result, err := s.DB.ExecContext(ctx, `INSERT INTO hhc_query.broker_settings(singleton,key_fingerprint) VALUES(true,$1) ON CONFLICT DO NOTHING`, s.Vault.Fingerprint())
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if n != 1 {
		return ErrDenied
	}
	return nil
}
func (s *PostgreSQL) SetPolicy(ctx context.Context, p Policy, expected int64) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	if !hashedCaller(p.CallerID) || expected < 0 || p.RequestsPerMinute < 1 || p.RequestsPerMinute > 1000 {
		return ErrDenied
	}
	switch p.HHCMode {
	case "disabled":
		if p.HHCGrant != "" || p.HHCQuota != "" {
			return ErrDenied
		}
	case "public":
		if p.HHCGrant != "" || !ownerID.MatchString(p.HHCQuota) {
			return ErrDenied
		}
	case "human":
		if !ownerID.MatchString(p.HHCGrant) || !ownerID.MatchString(p.HHCQuota) {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	switch p.YouTubeMode {
	case "disabled":
		if p.ChannelID != "" || p.YouTubeGrant != "" || p.YouTubeQuota != "" {
			return ErrDenied
		}
	case "public", "owner":
		if !youtube.ValidChannel(p.ChannelID) || !ownerID.MatchString(p.YouTubeGrant) || !ownerID.MatchString(p.YouTubeQuota) {
			return ErrDenied
		}
	default:
		return ErrDenied
	}
	var resultRows int64
	if expected == 0 {
		result, err := s.DB.ExecContext(ctx, `INSERT INTO hhc_query.caller_policy(caller_id,revision,enabled,hhc_mode,hhc_grant,hhc_quota,youtube_mode,youtube_grant,youtube_quota,channel_id,requests_per_minute) VALUES($1,1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, p.CallerID, p.Enabled, p.HHCMode, p.HHCGrant, p.HHCQuota, p.YouTubeMode, p.YouTubeGrant, p.YouTubeQuota, p.ChannelID, p.RequestsPerMinute)
		if err != nil {
			return ErrUnavailable
		}
		resultRows, err = result.RowsAffected()
		if err != nil {
			return ErrUnavailable
		}
	} else {
		result, err := s.DB.ExecContext(ctx, `UPDATE hhc_query.caller_policy SET revision=revision+1,enabled=$2,hhc_mode=$3,hhc_grant=$4,hhc_quota=$5,youtube_mode=$6,youtube_grant=$7,youtube_quota=$8,channel_id=$9,requests_per_minute=$10 WHERE caller_id=$1 AND revision=$11`, p.CallerID, p.Enabled, p.HHCMode, p.HHCGrant, p.HHCQuota, p.YouTubeMode, p.YouTubeGrant, p.YouTubeQuota, p.ChannelID, p.RequestsPerMinute, expected)
		if err != nil {
			return ErrUnavailable
		}
		resultRows, err = result.RowsAffected()
		if err != nil {
			return ErrUnavailable
		}
	}
	if resultRows != 1 {
		return ErrDenied
	}
	return nil
}
func (s *PostgreSQL) SetQuota(ctx context.Context, id, bucket string, seconds int, maximum int64) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	if !ownerID.MatchString(id) || maximum < 1 || seconds != 60 && seconds != 3600 && seconds != 86400 {
		return ErrDenied
	}
	if bucket != "hhc.read" && bucket != "youtube.search" && bucket != "youtube.default" || strings.HasPrefix(bucket, "youtube.") && seconds != 86400 {
		return ErrDenied
	}
	result, err := s.DB.ExecContext(ctx, `INSERT INTO hhc_query.quota_limit(quota_id,bucket,period_seconds,maximum) VALUES($1,$2,$3,$4) ON CONFLICT(quota_id,bucket) DO UPDATE SET maximum=excluded.maximum WHERE hhc_query.quota_limit.period_seconds=excluded.period_seconds`, id, bucket, seconds, maximum)
	if err != nil {
		return ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if n != 1 {
		return ErrDenied
	}
	return nil
}
func (s *PostgreSQL) RevokeCaller(ctx context.Context, id string) error {
	if err := s.Ready(ctx); err != nil {
		return err
	}
	if !hashedCaller(id) {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	// Every owner mutation that touches both authorities follows policy → grants.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_query.caller_policy SET enabled=false,revision=revision+1 WHERE caller_id=$1`, id); err != nil {
		return ErrUnavailable
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_query.grant_state SET state='revoked',updated_at=clock_timestamp() WHERE owner_id=$1`, id); err != nil {
		return ErrUnavailable
	}
	return safeCommit(tx)
}

func validSecrets(s Secrets) bool {
	if len(s.AccessToken) > 8192 || len(s.RefreshToken) > 8192 || len(s.ClientID) > 512 || len(s.ClientSecret) > 4096 {
		return false
	}
	for _, value := range []string{s.AccessToken, s.RefreshToken, s.ClientID, s.ClientSecret} {
		for _, b := range []byte(value) {
			if b < 0x21 || b > 0x7e {
				return false
			}
		}
	}
	return true
}
