package security

import "testing"

func TestPasswordHashAndCheck(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}
	if !CheckPassword(hash, "correct-password") {
		t.Fatal("expected password to match")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("expected password mismatch")
	}
}

func TestSecretEncryptionRoundTrip(t *testing.T) {
	ciphertext, nonce, err := EncryptSecret("test-vault-key", "upstream-secret")
	if err != nil {
		t.Fatalf("EncryptSecret returned error: %v", err)
	}
	plaintext, err := DecryptSecret("test-vault-key", ciphertext, nonce)
	if err != nil {
		t.Fatalf("DecryptSecret returned error: %v", err)
	}
	if plaintext != "upstream-secret" {
		t.Fatalf("unexpected plaintext %q", plaintext)
	}
}

func TestHashSecretStable(t *testing.T) {
	first := HashSecret("secret")
	second := HashSecret("secret")
	if first == "" || first != second {
		t.Fatal("expected stable non-empty hash")
	}
}
