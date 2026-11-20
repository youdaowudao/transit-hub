package secretbox

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestAbsorbCredentialAuthenticatedStorage(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	box, err := FromConfig(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"session":"isolated-fixture","unknown":{"keep":true}}`)
	aad := AAD("my_site_states.session", "fixture-user", "fixture-workspace")
	first, err := box.SealJSON(plain, aad)
	if err != nil || !IsSealedJSON(first) || bytes.Contains(first, plain) {
		t.Fatal("plaintext was not sealed as authenticated JSON")
	}
	second, err := box.SealJSON(plain, aad)
	if err != nil || bytes.Equal(first, second) {
		t.Fatal("nonce was reused")
	}
	got, err := box.OpenJSON(first, aad)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("stored JSON did not round-trip exactly")
	}
	wrong, _ := New(bytes.Repeat([]byte{8}, 32))
	if _, err := wrong.OpenJSON(first, aad); !errors.Is(err, ErrUndecryptable) {
		t.Fatal("wrong key was accepted")
	}
	for _, bad := range []string{AAD("my_site_states.session", "other", "fixture-workspace"), AAD("my_site_states.session", "fixture-user", "other"), AAD("notification_channel_settings.settings", "fixture-user", "fixture-workspace")} {
		if _, err := box.OpenJSON(first, bad); !errors.Is(err, ErrUndecryptable) {
			t.Fatal("a different storage identity was accepted")
		}
	}
	var absent *Box
	if _, err := absent.OpenJSON(first, aad); !errors.Is(err, ErrNoKey) {
		t.Fatal("missing key silently read ciphertext")
	}
	for _, reader := range []*Box{nil, box} {
		got, err := reader.OpenJSON(plain, aad)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatal("legacy plaintext was not preserved")
		}
	}
	if got, err := absent.SealJSON(plain, aad); err != nil || !bytes.Equal(got, plain) {
		t.Fatal("unconfigured writes changed legacy storage")
	}
	sealed, _ := box.Seal([]byte("fixture"), aad)
	raw, _ := base64.StdEncoding.DecodeString(sealed[len(Prefix):])
	raw[len(raw)-1] ^= 1
	if _, err := box.Open(Prefix+base64.StdEncoding.EncodeToString(raw), aad); !errors.Is(err, ErrUndecryptable) {
		t.Fatal("modified ciphertext was accepted")
	}
	for _, bad := range []string{Prefix, Prefix + "invalid", Prefix + "AAAA"} {
		if _, err := box.Open(bad, aad); !errors.Is(err, ErrUndecryptable) {
			t.Fatal("invalid ciphertext was accepted")
		}
	}
}

func TestAbsorbCredentialKeyAndIdentityValidation(t *testing.T) {
	for _, key := range []string{"invalid", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 33)), " " + base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		if _, err := FromConfig(key); !errors.Is(err, ErrInvalidKey) {
			t.Fatal("invalid configured key was accepted")
		}
	}
	if box, err := FromConfig(""); err != nil || box != nil {
		t.Fatal("unconfigured key should preserve legacy mode")
	}
	if AAD("table", "a:b", "c") == AAD("table", "a", "b:c") {
		t.Fatal("identity tuple separators collide")
	}
}
