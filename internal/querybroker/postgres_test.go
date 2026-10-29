package querybroker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos/youtube"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const fixtureResource = "https://query.example.invalid/mcp"

func testStore(t *testing.T) *PostgreSQL {
	t.Helper()
	raw := os.Getenv("HHC_QUERY_TEST_DATABASE_URL")
	if raw == "" {
		if os.Getenv("HHC_QUERY_REQUIRE_DATABASE_TESTS") == "1" {
			t.Fatal("query PostgreSQL fixture required")
		}
		t.Skip("query PostgreSQL fixture not selected")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/hhc_query_fixture" {
		t.Fatal("refuse non-fixture database")
	}
	q := u.Query()
	q.Set("passfile", os.DevNull)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslrootcert", "")
	u.RawQuery = q.Encode()
	config, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal("fixture configuration")
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`DROP SCHEMA IF EXISTS hhc_query CASCADE`); err != nil {
		t.Fatal("fixture connection", err)
	}
	migration, err := os.ReadFile("../../deploy/query/migrations/001_query_broker.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(bytes.Repeat([]byte{9}, 32), fixtureResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO hhc_query.broker_settings VALUES(true,$1)`, vault.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	store := &PostgreSQL{DB: db, Vault: vault}
	if err = store.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}
func seedPolicy(t *testing.T, s *PostgreSQL, id string) Policy {
	t.Helper()
	p := Policy{CallerID: strings.Repeat(id, 64), Revision: 1, Enabled: true, HHCMode: "public", HHCQuota: "hhc-project", YouTubeMode: "owner", YouTubeGrant: "yt-grant", YouTubeQuota: "yt-project", ChannelID: "UCabcdefghijklmnopqrstuv", RequestsPerMinute: 100}
	_, err := s.DB.Exec(`INSERT INTO hhc_query.caller_policy(caller_id,revision,enabled,hhc_mode,hhc_quota,youtube_mode,youtube_grant,youtube_quota,channel_id,requests_per_minute) VALUES($1,1,true,'public',$2,'owner',$3,$4,$5,100)`, p.CallerID, p.HHCQuota, p.YouTubeGrant, p.YouTubeQuota, p.ChannelID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ project, bucket string }{{p.HHCQuota, "hhc.read"}, {p.YouTubeQuota, "youtube.search"}, {p.YouTubeQuota, "youtube.default"}} {
		if _, err = s.DB.Exec(`INSERT INTO hhc_query.quota_limit(quota_id,bucket,period_seconds,maximum) VALUES($1,$2,86400,1000) ON CONFLICT DO NOTHING`, v.project, v.bucket); err != nil {
			t.Fatal(err)
		}
	}
	return p
}
func seedGrant(t *testing.T, s *PostgreSQL, p Policy) Grant {
	t.Helper()
	g := Grant{ID: p.YouTubeGrant, Owner: p.CallerID, Source: "youtube", Resource: p.ChannelID, Kind: "oauth", Scope: youtube.ReadScope, Generation: 1, State: "active", ExpiresAt: time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond), Secrets: Secrets{AccessToken: "expired-fixture", RefreshToken: "old-refresh-fixture", ClientID: "fixture-client"}}
	if err := s.ImportGrant(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	return g
}

type refreshFunc func(context.Context, Grant) (Secrets, time.Time, error)

func (f refreshFunc) Refresh(ctx context.Context, g Grant) (Secrets, time.Time, error) {
	return f(ctx, g)
}
func TestPostgresTwoReplicasRefreshOnceAndEncryptAtRest(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "a")
	g := seedGrant(t, s, p)
	// Independently opened database/sql pool, like another serving replica.
	// Use a separately opened pool through the same selected fixture DSN.
	raw := os.Getenv("HHC_QUERY_TEST_DATABASE_URL")
	u, _ := url.Parse(raw)
	q := u.Query()
	q.Set("passfile", os.DevNull)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslrootcert", "")
	u.RawQuery = q.Encode()
	config, _ := pgx.ParseConfig(u.String())
	other := stdlib.OpenDB(*config)
	other.SetMaxOpenConns(4)
	defer other.Close()
	replica := &PostgreSQL{DB: other, Vault: s.Vault}
	var count atomic.Int32
	refresh := refreshFunc(func(ctx context.Context, g Grant) (Secrets, time.Time, error) {
		count.Add(1)
		select {
		case <-ctx.Done():
			return Secrets{}, time.Time{}, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
		return Secrets{AccessToken: "fresh-fixture", RefreshToken: "new-refresh-fixture", ClientID: g.Secrets.ClientID}, time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond), nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := s
			if i%2 != 0 {
				store = replica
			}
			got, err := store.Credential(context.Background(), p, "youtube", p.ChannelID, refresh)
			if err != nil || got.Secrets.AccessToken != "fresh-fixture" || got.Generation != 2 {
				t.Errorf("coordinated refresh failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("refresh count", count.Load())
	}
	stored, encrypted, err := loadGrant(context.Background(), s.DB, g.ID)
	if err != nil || stored.State != "active" || stored.Generation != 2 || bytes.Contains(encrypted, []byte("refresh-fixture")) || bytes.Contains(encrypted, []byte("fresh-fixture")) {
		t.Fatal("encrypted grant invariant")
	}
	if err = s.Vault.open(&stored, encrypted); err != nil || stored.Secrets.RefreshToken != "new-refresh-fixture" {
		t.Fatal("rotated token was not durable")
	}
}
func TestPostgresLostRefreshIsFencedAndRevocationWins(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "b")
	seedGrant(t, s, p)
	var count atomic.Int32
	lost := refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
		count.Add(1)
		return Secrets{}, time.Time{}, errors.New("lost secret response")
	})
	for i := 0; i < 2; i++ {
		if _, err := s.Credential(context.Background(), p, "youtube", p.ChannelID, lost); !errors.Is(err, ErrReauthorize) {
			t.Fatal(err)
		}
	}
	if count.Load() != 1 {
		t.Fatal("consumed refresh replayed")
	}
	var state string
	s.DB.QueryRow(`SELECT state FROM hhc_query.grant_state WHERE grant_id=$1`, p.YouTubeGrant).Scan(&state)
	if state != "uncertain" {
		t.Fatal(state)
	}
	if _, err := s.DB.Exec(`UPDATE hhc_query.caller_policy SET enabled=false,revision=revision+1 WHERE caller_id=$1`, p.CallerID); err != nil {
		t.Fatal(err)
	}
	if err := s.Permit(context.Background(), p, "hhc", "content:videos", "authorization", 0); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestPostgresQuotaAtomicAcrossReplicasAndIsolation(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "c")
	if _, err := s.DB.Exec(`UPDATE hhc_query.quota_limit SET maximum=3 WHERE quota_id=$1 AND bucket='hhc.read'`, p.HHCQuota); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Permit(context.Background(), p, "hhc", "content:videos", "hhc.read", 1)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrQuota) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatal("quota overspend", accepted.Load())
	}
	var callerUsed int
	s.DB.QueryRow(`SELECT coalesce(sum(used),0) FROM hhc_query.quota_usage WHERE quota_key=$1`, "caller:"+p.CallerID).Scan(&callerUsed)
	if callerUsed != 3 {
		t.Fatal("denied global permits charged caller", callerUsed)
	}
	for _, tc := range []struct {
		source, res, bucket string
		cost                int
	}{{"youtube", "UC1234567890123456789012", "youtube.search", 1}, {"hhc", "content:videos", "youtube.search", 1}, {"hhc", "content:videos", "hhc.read", -1}} {
		if err := s.Permit(context.Background(), p, tc.source, tc.res, tc.bucket, tc.cost); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
	}
}
func TestVaultScopeAndEnvironmentIsolation(t *testing.T) {
	a, _ := NewVault(bytes.Repeat([]byte{1}, 32), fixtureResource)
	b, _ := NewVault(bytes.Repeat([]byte{1}, 32), "https://other.example.invalid/mcp")
	g := Grant{ID: "fixture", Owner: "a", Source: "youtube", Resource: "channel", Kind: "oauth", Scope: youtube.ReadScope, Generation: 1, State: "active", ExpiresAt: time.Now().UTC().Truncate(time.Microsecond), Secrets: Secrets{AccessToken: "sensitive-fixture"}}
	sealed, err := a.seal(g)
	if err != nil {
		t.Fatal(err)
	}
	copy := g
	if b.open(&copy, sealed) == nil {
		t.Fatal("ciphertext crossed environments")
	}
	for _, field := range []string{"owner", "resource", "generation", "state", "expiry"} {
		copy := g
		switch field {
		case "owner":
			copy.Owner = "other"
		case "resource":
			copy.Resource = "other"
		case "generation":
			copy.Generation++
		case "state":
			copy.State = "rotating"
		case "expiry":
			copy.ExpiresAt = copy.ExpiresAt.Add(time.Hour)
		}
		if a.open(&copy, sealed) == nil {
			t.Fatal("tampered", field)
		}
	}
}

func TestPostgresYouTubePacificMidnightAndDST(t *testing.T) {
	s := testStore(t)
	for _, tc := range []struct{ at, start, end string }{
		{"2026-03-08T07:59:59Z", "2026-03-07T08:00:00Z", "2026-03-08T08:00:00Z"},
		{"2026-03-08T08:00:00Z", "2026-03-08T08:00:00Z", "2026-03-09T07:00:00Z"},
		{"2026-11-01T07:00:00Z", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z"},
		{"2026-11-02T07:59:59Z", "2026-11-01T07:00:00Z", "2026-11-02T08:00:00Z"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		start, _ := time.Parse(time.RFC3339, tc.start)
		end, _ := time.Parse(time.RFC3339, tc.end)
		tx, err := s.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = spendAt(context.Background(), tx, tc.at, "youtube.search", 86400, 100, "America/Los_Angeles", &at); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var gotStart int64
		var gotEnd time.Time
		if err = s.DB.QueryRow(`SELECT window_start,expires_at FROM hhc_query.quota_usage WHERE quota_key=$1`, tc.at).Scan(&gotStart, &gotEnd); err != nil {
			t.Fatal(err)
		}
		if gotStart != start.Unix() || !gotEnd.Equal(end) {
			t.Fatalf("Pacific window at %s: %d %s", tc.at, gotStart, gotEnd)
		}
	}
}
func TestPostgresRevocationDuringRefresh(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "e")
	seedGrant(t, s, p)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	refresh := refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
		close(started)
		<-release
		return Secrets{AccessToken: "revoked-fixture", RefreshToken: "revoked-new-refresh"}, time.Now().Add(time.Hour), nil
	})
	go func() { _, err := s.Credential(context.Background(), p, "youtube", p.ChannelID, refresh); done <- err }()
	<-started
	if _, err := s.DB.Exec(`UPDATE hhc_query.grant_state SET state='revoked' WHERE grant_id=$1`, p.YouTubeGrant); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrReauthorize) {
		t.Fatal("revoked refresh returned token", err)
	}
}

func TestPostgresRevocationWaitsForBlockedPermit(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "f")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Pre-lock the shared quota row so a permit blocks after acquiring authority.
	if err := s.Permit(ctx, p, "hhc", "content:videos", "hhc.read", 1); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `SELECT 1 FROM hhc_query.quota_usage WHERE quota_key=$1 FOR UPDATE`, "project:"+p.HHCQuota); err != nil {
		t.Fatal(err)
	}
	permitDone := make(chan error, 1)
	go func() { permitDone <- s.Permit(ctx, p, "hhc", "content:videos", "hhc.read", 1) }()
	// Inspect actual row-lock waiting, not a timing-only sleep assumption.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		err = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%INSERT INTO hhc_query.quota_usage%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("permit did not reach blocked quota")
		}
		time.Sleep(10 * time.Millisecond)
	}
	revokeDone := make(chan error, 1)
	go func() {
		_, err := s.DB.ExecContext(ctx, `UPDATE hhc_query.caller_policy SET enabled=false,revision=revision+1 WHERE caller_id=$1`, p.CallerID)
		revokeDone <- err
	}()
	select {
	case <-revokeDone:
		t.Fatal("revocation bypassed the in-flight authority lock")
	case <-time.After(40 * time.Millisecond):
	}
	if err = blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-permitDone; err != nil {
		t.Fatal(err)
	}
	if err = <-revokeDone; err != nil {
		t.Fatal(err)
	}
	if err = s.Permit(ctx, p, "hhc", "content:videos", "authorization", 0); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked authority survived", err)
	}
}
func TestPolicyLockFunctionDoesNotNeedPolicyWriteRights(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "1")
	if _, err := s.DB.Exec(`DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='hhc_query_fixture_runtime') THEN CREATE ROLE hhc_query_fixture_runtime NOLOGIN; END IF; END $$;
 GRANT USAGE ON SCHEMA hhc_query TO hhc_query_fixture_runtime;
 GRANT SELECT ON hhc_query.caller_policy TO hhc_query_fixture_runtime;
 GRANT EXECUTE ON FUNCTION hhc_query.lock_policy(text) TO hhc_query_fixture_runtime;`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SET LOCAL ROLE hhc_query_fixture_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err = loadPolicyLocked(context.Background(), tx, p.CallerID); err != nil {
		t.Fatal("read-lock function failed", err)
	}
	if _, err = tx.Exec(`UPDATE hhc_query.caller_policy SET enabled=false WHERE caller_id=$1`, p.CallerID); err == nil {
		t.Fatal("runtime unexpectedly has policy write rights")
	}
}

func TestOwnerImportRefusesWrongEnvironmentKey(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "2")
	wrong, _ := NewVault(bytes.Repeat([]byte{7}, 32), fixtureResource)
	owner := &PostgreSQL{DB: s.DB, Vault: wrong}
	g := Grant{ID: "must-not-persist", Owner: p.CallerID, Source: "youtube", Resource: p.ChannelID, Kind: "oauth", Scope: youtube.ReadScope, Generation: 1, State: "active", ExpiresAt: time.Now().Add(time.Hour), Secrets: Secrets{AccessToken: "fixture-token", RefreshToken: "fixture-refresh", ClientID: "fixture-client"}}
	if owner.ImportGrant(context.Background(), g) == nil {
		t.Fatal("wrong master key import succeeded")
	}
	var count int
	s.DB.QueryRow(`SELECT count(*) FROM hhc_query.grant_state WHERE grant_id=$1`, g.ID).Scan(&count)
	if count != 0 {
		t.Fatal("wrong-key ciphertext persisted")
	}
}
func TestOwnerPolicyCASAndRevocation(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "3")
	seedGrant(t, s, p)
	p.RequestsPerMinute = 20
	if err := s.SetPolicy(context.Background(), p, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPolicy(context.Background(), p, 1); !errors.Is(err, ErrDenied) {
		t.Fatal("stale owner policy update accepted")
	}
	if err := s.RevokeCaller(context.Background(), p.CallerID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Policy(context.Background(), p.CallerID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	var state string
	s.DB.QueryRow(`SELECT state FROM hhc_query.grant_state WHERE grant_id=$1`, p.YouTubeGrant).Scan(&state)
	if state != "revoked" {
		t.Fatal("private grant survived caller revocation")
	}
}

func TestPostgresNanosecondExpiryRoundTrips(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprint(refresh), func(t *testing.T) {
			s := testStore(t)
			p := seedPolicy(t, s, "4")
			expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
			if refresh {
				expiry = expiry.Add(-2 * time.Hour)
			}
			g := Grant{ID: p.YouTubeGrant, Owner: p.CallerID, Source: "youtube", Resource: p.ChannelID, Kind: "oauth", Scope: youtube.ReadScope, Generation: 1, State: "active", ExpiresAt: expiry, Secrets: Secrets{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ClientID: "fixture-client"}}
			if err := s.ImportGrant(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			nextExpiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(987654321 * time.Nanosecond)
			refresher := refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
				return Secrets{AccessToken: "fixture-new-access", RefreshToken: "fixture-new-refresh", ClientID: "fixture-client"}, nextExpiry, nil
			})
			for i := 0; i < 2; i++ {
				got, err := s.Credential(context.Background(), p, "youtube", p.ChannelID, refresher)
				if err != nil {
					t.Fatal("precision round trip", err)
				}
				want := expiry
				if refresh {
					want = nextExpiry
				}
				if !got.ExpiresAt.Equal(want.Truncate(time.Microsecond)) {
					t.Fatal("expiry precision mismatch")
				}
			}
		})
	}
}
func TestImportRejectsControlBytes(t *testing.T) {
	g := Grant{ID: "fixture", Owner: strings.Repeat("a", 64), Source: "youtube", Resource: "UCabcdefghijklmnopqrstuv", Kind: "oauth", Scope: youtube.ReadScope, Generation: 1, State: "active", ExpiresAt: time.Now().Add(time.Hour), Secrets: Secrets{AccessToken: "fixture", RefreshToken: "refresh", ClientID: "client"}}
	for _, control := range []byte{0, 9, 10, 13, 31, 32, 127} {
		copy := g
		copy.Secrets.AccessToken += "x" + string(control)
		if ValidateImport(copy) == nil {
			t.Fatal("control byte accepted", control)
		}
	}
}

func TestRefreshIdentityCannotBeReimportedUnderAnotherGrant(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "5")
	g := seedGrant(t, s, p)
	clone := g
	clone.ID = "duplicate-grant"
	if err := s.ImportGrant(context.Background(), clone); !errors.Is(err, ErrDenied) {
		t.Fatal("duplicate provider credential import", err)
	}
	_, err := s.Credential(context.Background(), p, "youtube", p.ChannelID, refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
		return Secrets{}, time.Time{}, ErrUnavailable
	}))
	if !errors.Is(err, ErrReauthorize) {
		t.Fatal(err)
	}
	clone.ID = "uncertain-retry"
	if err := s.ImportGrant(context.Background(), clone); !errors.Is(err, ErrDenied) {
		t.Fatal("uncertain refresh escaped fence via new ID", err)
	}
	if _, err = s.DB.Exec(`DELETE FROM hhc_query.grant_state WHERE grant_id=$1`, g.ID); err != nil {
		t.Fatal(err)
	}
	clone.ID = "deleted-grant-retry"
	if err := s.ImportGrant(context.Background(), clone); !errors.Is(err, ErrDenied) {
		t.Fatal("grant deletion erased consumed refresh identity", err)
	}
	var count int
	s.DB.QueryRow(`SELECT count(*) FROM hhc_query.refresh_identity`).Scan(&count)
	if count != 1 {
		t.Fatal("refresh identity tombstone missing", count)
	}
}

func TestHistoricalRefreshIdentityCannotBeReactivated(t *testing.T) {
	s := testStore(t)
	p := seedPolicy(t, s, "6")
	g := seedGrant(t, s, p)
	r1 := g.Secrets.RefreshToken
	first := refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
		return Secrets{AccessToken: "fixture-access-r2", RefreshToken: "fixture-r2", ClientID: g.Secrets.ClientID}, time.Now().Add(time.Hour), nil
	})
	if _, err := s.Credential(context.Background(), p, "youtube", p.ChannelID, first); err != nil {
		t.Fatal(err)
	}
	current, encrypted, err := loadGrant(context.Background(), s.DB, g.ID)
	if err != nil || s.Vault.open(&current, encrypted) != nil {
		t.Fatal("fixture reload")
	}
	current.ExpiresAt = time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	encrypted, err = s.Vault.seal(current)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE hhc_query.grant_state SET expires_at=$2,ciphertext=$3 WHERE grant_id=$1`, g.ID, current.ExpiresAt, encrypted); err != nil {
		t.Fatal(err)
	}
	historical := refreshFunc(func(context.Context, Grant) (Secrets, time.Time, error) {
		return Secrets{AccessToken: "must-not-publish", RefreshToken: r1, ClientID: g.Secrets.ClientID}, time.Now().Add(time.Hour), nil
	})
	if _, err = s.Credential(context.Background(), p, "youtube", p.ChannelID, historical); !errors.Is(err, ErrReauthorize) {
		t.Fatal("historical refresh revived", err)
	}
	stored, _, err := loadGrant(context.Background(), s.DB, g.ID)
	if err != nil || stored.State != "uncertain" {
		t.Fatal("historical refresh was published")
	}
}
