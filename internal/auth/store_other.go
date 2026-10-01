//go:build (!windows && !darwin) || (darwin && !cgo)

package auth

func loadNativeSecret(string) ([]byte, error) { return nil, ErrCredentialStoreUnavailable }
func saveNativeSecret(string, []byte) error   { return ErrCredentialStoreUnavailable }
func deleteNativeSecret(string) error         { return ErrCredentialStoreUnavailable }
