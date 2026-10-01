package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-cli/internal/operation"
)

type credentialStore interface {
	Load(string) ([]byte, error)
	Save(string, []byte) error
	Delete(string) error
}

// savedProfile is encoded only into the OS credential store. Rotating is a
// durable fence: a crash or lost response must not replay a consumed refresh.
type savedProfile struct {
	Version   int       `json:"version"`
	Kind      string    `json:"kind"`
	ClientID  string    `json:"clientId,omitempty"`
	DeviceID  string    `json:"deviceId,omitempty"`
	Scope     string    `json:"scope"`
	Secret    string    `json:"secret"`
	Rotating  bool      `json:"rotating,omitempty"`
	Principal Principal `json:"principal"`
}

func (savedProfile) String() string     { return "[redacted profile credential]" }
func (p savedProfile) GoString() string { return p.String() }

type Profiles struct {
	directory string
	store     credentialStore
	human     *HumanClient
	service   *ServiceClient
}

func NewProfiles(directory string) *Profiles {
	return &Profiles{directory: directory, store: NativeStore{}, human: NewHumanClient(), service: NewServiceClient()}
}

func (p *Profiles) lock(ctx context.Context, profile string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidProfile(profile) || !filepath.IsAbs(p.directory) {
		return nil, ErrInvalidAuthInput
	}
	if err := os.MkdirAll(p.directory, 0700); err != nil {
		return nil, ErrCredentialStoreUnavailable
	}
	info, err := os.Lstat(p.directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrCredentialStoreUnavailable
	}
	root, err := os.OpenRoot(p.directory)
	if err != nil {
		return nil, ErrCredentialStoreUnavailable
	}
	defer root.Close()
	if err := root.Mkdir(profile, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrCredentialStoreUnavailable
	}
	return operation.LockWorkspace(filepath.Join(p.directory, profile))
}

func (p *Profiles) load(profile string) (savedProfile, error) {
	data, err := p.store.Load(profile)
	if errors.Is(err, ErrCredentialNotFound) {
		return savedProfile{}, ErrAuthenticationRequired
	}
	if err != nil {
		return savedProfile{}, err
	}
	defer clear(data)
	var value savedProfile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > 2560 || decoder.Decode(&value) != nil || value.Version != 1 || value.Secret == "" {
		return savedProfile{}, ErrCredentialStoreUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return savedProfile{}, ErrCredentialStoreUnavailable
	}
	if value.Kind == "human" {
		if !validDeviceID(value.DeviceID) || value.ClientID != "" || !bearerPattern.MatchString(value.Secret) {
			return savedProfile{}, ErrCredentialStoreUnavailable
		}
	} else if value.Kind == "service" {
		if value.ClientID == "" || value.DeviceID != "" || value.Rotating {
			return savedProfile{}, ErrCredentialStoreUnavailable
		}
	} else {
		return savedProfile{}, ErrCredentialStoreUnavailable
	}
	return value, nil
}

func (p *Profiles) save(profile string, value savedProfile) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	defer clear(data)
	if len(data) > 2560 {
		return ErrInvalidAuthInput
	}
	return p.store.Save(profile, data)
}

func (p *Profiles) Token(ctx context.Context, profile string) (Token, error) {
	lock, err := p.lock(ctx, profile)
	if err != nil {
		return Token{}, err
	}
	defer lock.Close()
	value, err := p.load(profile)
	if err != nil {
		return Token{}, err
	}
	if value.Kind == "service" {
		token, err := p.service.Exchange(ctx, value.ClientID, value.Secret, strings.Fields(value.Scope))
		if err != nil {
			return Token{}, err
		}
		if !samePrincipal(value.Principal, token.Principal()) {
			return Token{}, ErrAuthenticationRequired
		}
		return token, nil
	}
	if value.Rotating {
		return Token{}, ErrAuthenticationRequired
	}
	value.Rotating = true
	if err := p.save(profile, value); err != nil {
		return Token{}, err
	}
	credentials, err := p.human.Refresh(ctx, HumanCredentials{access: Token{scope: value.Scope}, refresh: value.Secret}, value.DeviceID)
	if err != nil {
		return Token{}, err
	}
	if !samePrincipal(value.Principal, credentials.AccessToken().Principal()) {
		return Token{}, ErrAuthenticationRequired
	}
	value.Secret = credentials.RefreshToken()
	value.Principal = credentials.AccessToken().Principal()
	value.Scope = credentials.AccessToken().Scope()
	value.Rotating = false
	if err := p.save(profile, value); err != nil {
		return Token{}, err
	}
	return credentials.AccessToken(), nil
}

func (p *Profiles) LoginService(ctx context.Context, profile, clientID, secret string, scopes []string) (Token, error) {
	lock, err := p.lock(ctx, profile)
	if err != nil {
		return Token{}, err
	}
	defer lock.Close()
	token, err := p.service.Exchange(ctx, clientID, secret, scopes)
	if err != nil {
		return Token{}, err
	}
	if err := p.save(profile, savedProfile{Version: 1, Kind: "service", ClientID: clientID, Scope: token.Scope(), Secret: secret, Principal: token.Principal()}); err != nil {
		return Token{}, err
	}
	return token, nil
}

func (p *Profiles) LoginHuman(ctx context.Context, profile string, options HumanLoginOptions) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	if options.NoInput {
		return Token{}, ErrAuthenticationRequired
	}
	lock, err := p.lock(ctx, profile)
	if err != nil {
		return Token{}, err
	}
	defer lock.Close()
	previous, err := p.load(profile)
	if err != nil && !errors.Is(err, ErrAuthenticationRequired) {
		return Token{}, err
	}
	options.DeviceID = randomPKCEValue()
	if previous.Kind == "human" {
		options.DeviceID = previous.DeviceID
	}
	credentials, err := p.human.Login(ctx, options)
	if err != nil {
		return Token{}, err
	}
	value := savedProfile{Version: 1, Kind: "human", DeviceID: options.DeviceID, Scope: credentials.AccessToken().Scope(), Secret: credentials.RefreshToken(), Principal: credentials.AccessToken().Principal()}
	if err := p.save(profile, value); err != nil {
		return Token{}, err
	}
	return credentials.AccessToken(), nil
}

func samePrincipal(saved, issued Principal) bool {
	return saved.ID == issued.ID && saved.Type == issued.Type && saved.ClientID == issued.ClientID && saved.CredentialID == issued.CredentialID
}

type LogoutResult struct {
	LocalCleared  bool `json:"localCleared"`
	RemoteRevoked bool `json:"remoteRevoked"`
}

func (p *Profiles) Logout(ctx context.Context, profile string) (LogoutResult, error) {
	var result LogoutResult
	lock, err := p.lock(ctx, profile)
	if err != nil {
		return result, err
	}
	defer lock.Close()
	value, loadErr := p.load(profile)
	var remoteErr error
	if loadErr == nil && value.Kind == "human" {
		remoteErr = p.human.Revoke(ctx, HumanCredentials{refresh: value.Secret})
		result.RemoteRevoked = remoteErr == nil
	}
	if err := p.store.Delete(profile); err != nil {
		return result, err
	}
	result.LocalCleared = true
	if loadErr != nil && !errors.Is(loadErr, ErrAuthenticationRequired) {
		return result, loadErr
	}
	return result, remoteErr
}
