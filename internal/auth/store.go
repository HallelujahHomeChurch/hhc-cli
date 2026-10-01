package auth

import "errors"

var (
	ErrCredentialNotFound         = errors.New("credential_not_found")
	ErrCredentialStoreUnavailable = errors.New("credential_store_unavailable")
)

// NativeStore uses the current OS user's credential store, with no file fallback
// or interactive prompts. Profile serialization is the profile owner's job.
type NativeStore struct{}

func validProfile(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '-' || c == '_') {
			continue
		}
		return false
	}
	return true
}

func (NativeStore) Load(profile string) ([]byte, error) {
	if !validProfile(profile) {
		return nil, ErrInvalidAuthInput
	}
	return loadNativeSecret(profile)
}

func (NativeStore) Save(profile string, value []byte) error {
	// Windows generic credentials have a 2,560-byte limit. Use one atomic native
	// item for the whole profile credential; never split a rotated secret.
	if !validProfile(profile) || len(value) == 0 || len(value) > 2560 {
		return ErrInvalidAuthInput
	}
	return saveNativeSecret(profile, value)
}

func (NativeStore) Delete(profile string) error {
	if !validProfile(profile) {
		return ErrInvalidAuthInput
	}
	err := deleteNativeSecret(profile)
	if errors.Is(err, ErrCredentialNotFound) {
		return nil
	}
	return err
}
