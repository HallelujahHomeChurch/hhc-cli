package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestStoreRejectsAmbiguousProfilesBeforeNativeAccess(t *testing.T) {
	store := NativeStore{}
	for _, name := range []string{"", "Default", "../default", "a/b", "a\\b", "a\x00b", "個人"} {
		if err := store.Save(name, []byte("fixture")); !errors.Is(err, ErrInvalidAuthInput) {
			t.Errorf("save %q: %v", name, err)
		}
		if _, err := store.Load(name); !errors.Is(err, ErrInvalidAuthInput) {
			t.Errorf("load %q: %v", name, err)
		}
		if err := store.Delete(name); !errors.Is(err, ErrInvalidAuthInput) {
			t.Errorf("delete %q: %v", name, err)
		}
	}
	for _, size := range []int{0, 2561} {
		if err := store.Save("default", make([]byte, size)); !errors.Is(err, ErrInvalidAuthInput) {
			t.Errorf("size %d: %v", size, err)
		}
	}
}

func TestNativeCredentialRoundTripAndRotation(t *testing.T) {
	if os.Getenv("HHC_TEST_NATIVE_CREDENTIALS") != "1" {
		t.Skip("native credential store acceptance requires explicit test context")
	}
	var suffix [16]byte
	rand.Read(suffix[:])
	name := "test-" + hex.EncodeToString(suffix[:])
	store := NativeStore{}
	if _, err := store.Load(name); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("missing credential: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Delete(name); err != nil {
			t.Errorf("test credential cleanup: %v", err)
		}
	})
	for _, secret := range [][]byte{[]byte("fixture-one\x00中文"), bytes.Repeat([]byte{0xA5}, 2560)} {
		if err := store.Save(name, secret); err != nil {
			t.Fatalf("native save: %v", err)
		}
		actual, err := store.Load(name)
		if err != nil || !bytes.Equal(actual, secret) {
			t.Fatalf("native roundtrip or rotation failed: %v", err)
		}
	}
	if err := store.Delete(name); err != nil {
		t.Fatalf("native delete: %v", err)
	}
	if _, err := store.Load(name); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("credential still present: %v", err)
	}
	if err := store.Delete(name); err != nil {
		t.Fatalf("repeated logout: %v", err)
	}
}
