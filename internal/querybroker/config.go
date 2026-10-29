package querybroker

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Config is public routing/identity configuration, not a credential file.
type Config struct {
	ResourceURL      string   `json:"resourceUrl"`
	Issuer           string   `json:"issuer"`
	IntrospectionURL string   `json:"introspectionUrl"`
	RequiredScope    string   `json:"requiredScope"`
	ClientID         string   `json:"clientId"`
	Hosts            []string `json:"hosts"`
	Origins          []string `json:"origins"`
}
type RuntimeSecrets struct {
	DatabaseURL         string `json:"databaseUrl"`
	MasterKey           string `json:"masterKey"`
	IntrospectionSecret string `json:"introspectionSecret"`
}

func (RuntimeSecrets) String() string     { return "[redacted runtime secrets]" }
func (s RuntimeSecrets) GoString() string { return s.String() }
func LoadConfig(path string) (Config, error) {
	var out Config
	file, err := os.Open(path)
	if err != nil {
		return out, ErrUnavailable
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 {
		return out, ErrUnavailable
	}
	if strictJSON(raw, &out) != nil {
		return out, ErrUnavailable
	}
	return out, nil
}

// LoadRuntimeSecrets accepts only an inherited private pipe or explicit platform
// secret injection. No .env, keychain, home-directory or plaintext-file fallback.
func LoadRuntimeSecrets() (RuntimeSecrets, error) {
	var raw []byte
	var out RuntimeSecrets
	fdText := os.Getenv("HHC_QUERY_SECRET_FD")
	injected := os.Getenv("HHC_QUERY_RUNTIME_SECRET")
	if fdText != "" && injected != "" {
		return out, ErrUnavailable
	}
	if fdText != "" {
		fd, err := strconv.Atoi(fdText)
		if err != nil || fd < 3 {
			return out, ErrUnavailable
		}
		file := os.NewFile(uintptr(fd), "query-runtime-secret")
		if file == nil {
			return out, ErrUnavailable
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 || info.Mode().Perm()&0077 != 0 {
			return out, ErrUnavailable
		}
		raw, err = io.ReadAll(io.LimitReader(file, 32769))
		if err != nil {
			return out, ErrUnavailable
		}
	} else if injected != "" {
		raw = []byte(injected)
		os.Unsetenv("HHC_QUERY_RUNTIME_SECRET")
	} else {
		return out, ErrUnavailable
	}
	defer clear(raw)
	if len(raw) > 32768 || strictJSON(raw, &out) != nil || out.DatabaseURL == "" {
		return RuntimeSecrets{}, ErrUnavailable
	}
	return out, nil
}
func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrUnavailable
	}
	return nil
}
func Compose(config Config, secrets RuntimeSecrets) (*Backend, *sql.DB, error) {
	if !secureURL(config.ResourceURL) || !strings.HasSuffix(config.ResourceURL, "/mcp") || len(config.Hosts) == 0 {
		return nil, nil, ErrUnavailable
	}
	master, err := base64.StdEncoding.DecodeString(secrets.MasterKey)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	defer clear(master)
	vault, err := NewVault(master, config.ResourceURL)
	if err != nil {
		return nil, nil, err
	}
	auth, err := NewIntrospector(config.Issuer, config.IntrospectionURL, config.ResourceURL, config.RequiredScope, config.ClientID, secrets.IntrospectionSecret)
	if err != nil {
		return nil, nil, err
	}
	db, err := OpenDatabase(secrets.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	store := &PostgreSQL{DB: db, Vault: vault}
	return &Backend{Auth: auth, Store: store, Vault: vault, Refresh: NewGoogleRefresh()}, db, nil
}
func OpenDatabase(raw string) (*sql.DB, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Hostname() == "" || strings.Contains(u.Hostname(), ",") || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return nil, ErrUnavailable
	}
	password, ok := u.User.Password()
	if !ok || password == "" || u.Query().Get("sslmode") != "verify-full" {
		return nil, ErrUnavailable
	}
	// Refuse ambient libpq identities and file selectors. The caller must supply
	// the complete approved connection through injection, never ~/.pgpass.
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "PG") {
			return nil, ErrUnavailable
		}
	}
	q := u.Query()
	for name := range q {
		if name != "sslmode" {
			return nil, ErrUnavailable
		}
	}
	q.Set("passfile", os.DevNull)
	q.Set("sslcert", "")
	q.Set("sslkey", "")
	q.Set("sslrootcert", "system")
	q.Set("connect_timeout", "5")
	q.Set("application_name", "hhc-query")
	u.RawQuery = q.Encode()
	config, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, ErrUnavailable
	}
	if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || len(config.Fallbacks) > 0 {
		return nil, ErrUnavailable
	}
	config.RuntimeParams["synchronous_commit"] = "on"
	config.RuntimeParams["statement_timeout"] = "5000"
	config.RuntimeParams["lock_timeout"] = "1000"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}
