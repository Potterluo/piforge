package crypto

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"testing"
)

func base64Std(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, secret := range []string{"sk-ant-api03-abcdef", "", "短密钥", string(bytes.Repeat([]byte("x"), 4096))} {
		sealed, err := box.SealString(secret)
		if err != nil {
			t.Fatalf("Seal(%q): %v", secret, err)
		}
		if bytes.Contains(sealed, []byte(secret)) && secret != "" {
			t.Fatalf("ciphertext contains the plaintext for %q", secret)
		}
		got, err := box.OpenString(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != secret {
			t.Fatalf("round trip = %q, want %q", got, secret)
		}
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	box, _ := New(bytes.Repeat([]byte{1}, KeySize))
	first, _ := box.SealString("same input")
	second, _ := box.SealString("same input")
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same plaintext are identical; the nonce is not random")
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	good, _ := New(bytes.Repeat([]byte{1}, KeySize))
	bad, _ := New(bytes.Repeat([]byte{2}, KeySize))

	sealed, err := good.SealString("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := bad.OpenString(sealed); err == nil {
		t.Fatal("decrypting with the wrong key succeeded")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	box, _ := New(bytes.Repeat([]byte{3}, KeySize))
	sealed, _ := box.SealString("secret")

	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.OpenString(sealed); err == nil {
		t.Fatal("a tampered ciphertext decrypted")
	}

	if _, err := box.OpenString([]byte{1, 2, 3}); err == nil {
		t.Fatal("a truncated ciphertext decrypted")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("accepted a short key")
	}
}

func TestNewFromEnvGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIFORGE_MASTER_KEY", "")

	first, err := NewFromEnv(dir)
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	sealed, err := first.SealString("persisted")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// A second boot must find the same key file and open the ciphertext.
	second, err := NewFromEnv(dir)
	if err != nil {
		t.Fatalf("NewFromEnv (second): %v", err)
	}
	got, err := second.OpenString(sealed)
	if err != nil {
		t.Fatalf("Open with the reloaded key: %v", err)
	}
	if got != "persisted" {
		t.Fatalf("round trip across restarts = %q", got)
	}

	if filepath.Base(dir) == "" {
		t.Fatal("unreachable")
	}
}

func TestNewFromEnvPrefersTheVariable(t *testing.T) {
	dir := t.TempDir()
	box, err := New(bytes.Repeat([]byte{9}, KeySize))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sealed, _ := box.SealString("from-env")

	t.Setenv("PIFORGE_MASTER_KEY", base64Std(bytes.Repeat([]byte{9}, KeySize)))
	fromEnv, err := NewFromEnv(dir)
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if got, err := fromEnv.OpenString(sealed); err != nil || got != "from-env" {
		t.Fatalf("env key did not open the ciphertext: %q %v", got, err)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"sk-ant-api03-abcdef": "••••cdef",
		"abc":                 "••••",
		"":                    "••••",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
