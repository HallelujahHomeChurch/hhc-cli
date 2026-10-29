package querybroker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/queryruntime"
	"github.com/HallelujahHomeChurch/hhc-cli/internal/videos"
)

// Backend is embedded in each query process. Only PostgreSQL and the selected
// OAuth authority are shared. Binding signatures use an injected common key;
// a query never relies on replica-local authorization/token caches.
type Backend struct {
	Auth    Authenticator
	Store   Store
	Vault   *Vault
	Refresh Refresher
}
type bindingClaims struct {
	Caller    string `json:"c"`
	Revision  int64  `json:"r"`
	Tool      string `json:"t"`
	Expires   int64  `json:"e"`
	TokenHash string `json:"h"`
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (b *Backend) Ready(ctx context.Context) error {
	if b == nil || b.Auth == nil || b.Store == nil || b.Vault == nil {
		return failure(ErrUnavailable)
	}
	return failure(b.Store.Ready(ctx))
}
func (b *Backend) Authorize(ctx context.Context, bearer, tool string) (queryruntime.Binding, error) {
	var out queryruntime.Binding
	switch tool {
	case "initialize", "ping", "tools/list", "notifications/initialized", "notifications/cancelled", "search_videos", "get_video", "compare_publication":
	default:
		return out, failure(ErrDenied)
	}
	principal, err := b.Auth.Authenticate(ctx, bearer)
	if err != nil {
		return out, failure(err)
	}
	p, err := b.Store.Policy(ctx, CallerID(principal))
	if err != nil {
		return out, failure(err)
	}
	if err = b.Store.Permit(ctx, p, "", "", "mcp.requests", 1); err != nil {
		return out, failure(err)
	}
	claims := bindingClaims{Caller: p.CallerID, Revision: p.Revision, Tool: tool, Expires: min(principal.ExpiresAt.Unix(), time.Now().Add(35*time.Second).Unix()), TokenHash: hashToken(bearer)}
	raw, _ := json.Marshal(claims)
	mac := hmac.New(sha256.New, b.Vault.bindingKey)
	mac.Write(raw)
	out.BindingID = base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	out.CallerID = p.CallerID
	out.HHC.Enabled = p.HHCMode == "public" || p.HHCMode == "human"
	out.HHC.Access = p.HHCMode
	out.YouTube.Enabled = p.YouTubeMode == "public" || p.YouTubeMode == "owner"
	out.YouTube.Access = p.YouTubeMode
	out.YouTube.ChannelID = p.ChannelID
	return out, nil
}
func (b *Backend) validate(ctx context.Context, binding queryruntime.Binding, bearer, source, res string) (Policy, error) {
	encoded, signature, ok := strings.Cut(binding.BindingID, ".")
	if !ok || len(binding.BindingID) > 2048 {
		return Policy{}, callerFailure(ErrDenied)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Policy{}, callerFailure(ErrDenied)
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return Policy{}, callerFailure(ErrDenied)
	}
	mac := hmac.New(sha256.New, b.Vault.bindingKey)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Policy{}, callerFailure(ErrDenied)
	}
	var claims bindingClaims
	if json.Unmarshal(raw, &claims) != nil || claims.Caller != binding.CallerID || claims.TokenHash != hashToken(bearer) {
		return Policy{}, callerFailure(ErrDenied)
	}
	if claims.Expires <= time.Now().Unix() {
		return Policy{}, callerFailure(ErrReauthorize)
	}
	principal, err := b.Auth.Authenticate(ctx, bearer)
	if err != nil {
		return Policy{}, callerFailure(err)
	}
	if CallerID(principal) != claims.Caller {
		return Policy{}, callerFailure(ErrDenied)
	}
	p, err := b.Store.Policy(ctx, claims.Caller)
	if err != nil {
		return p, callerFailure(err)
	}
	if p.Revision != claims.Revision {
		return p, callerFailure(ErrDenied)
	}
	switch claims.Tool {
	case "search_videos", "get_video", "compare_publication":
	default:
		return p, callerFailure(ErrDenied)
	}
	if _, _, err = resource(p, source, res); err != nil {
		return p, err
	}
	return p, nil
}
func (b *Backend) Credential(ctx context.Context, binding queryruntime.Binding, bearer, source, res string) (queryruntime.Credential, error) {
	var out queryruntime.Credential
	p, err := b.validate(ctx, binding, bearer, source, res)
	if err != nil {
		return out, failure(err)
	}
	g, err := b.Store.Credential(ctx, p, source, res, b.Refresh)
	if err != nil {
		return out, failure(err)
	}
	out = queryruntime.Credential{CallerID: p.CallerID, BindingID: binding.BindingID, Source: source, Resource: res, Kind: g.Kind, Scope: g.Scope, Token: g.Secrets.AccessToken, ExpiresAt: g.ExpiresAt}
	return out, nil
}
func (b *Backend) Permit(ctx context.Context, binding queryruntime.Binding, bearer, source, res, bucket string, cost int) error {
	p, err := b.validate(ctx, binding, bearer, source, res)
	if err != nil {
		return failure(err)
	}
	return failure(b.Store.Permit(ctx, p, source, res, bucket, cost))
}
func failure(err error) error {
	if err == nil {
		return nil
	}
	code := "broker_unavailable"
	switch {
	case errors.Is(err, ErrScope):
		code = "insufficient_scope"
	case errors.Is(err, ErrDenied):
		code = "permission_denied"
	case errors.Is(err, ErrReauthorize):
		code = "authentication_required"
	case errors.Is(err, ErrQuota):
		code = "rate_limited"
	}
	result := videos.SafeFailure(videos.FailureCode(code))
	result.CallerAuthority = errors.Is(err, ErrCallerChanged)
	return result
}
