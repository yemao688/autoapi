package service

import (
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	// We need a store with an in-memory DB to test service methods that
	// depend on master password storage. For pure crypto tests, we bypass
	// by directly setting encKey.
	svc := &Service{}
	svc.encKey = make([]byte, 32)
	// Use a fixed key for deterministic test (in production it's derived)
	for i := range svc.encKey {
		svc.encKey[i] = byte(i)
	}

	plaintext := []byte("hello, this is a secret API key: sk-abc123")

	ciphertext, nonce, err := svc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}
	if len(nonce) == 0 {
		t.Fatal("nonce is empty")
	}

	decrypted, err := svc.Decrypt(ciphertext, nonce)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted text mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestDecryptWrongKey(t *testing.T) {
	// Encrypt with one key
	svc1 := &Service{}
	svc1.encKey = make([]byte, 32)
	for i := range svc1.encKey {
		svc1.encKey[i] = byte(i)
	}

	plaintext := []byte("secret data")
	ciphertext, nonce, err := svc1.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Decrypt with a different key
	svc2 := &Service{}
	svc2.encKey = make([]byte, 32)
	for i := range svc2.encKey {
		svc2.encKey[i] = byte(255 - i) // different key
	}

	_, err = svc2.Decrypt(ciphertext, nonce)
	if err == nil {
		t.Fatal("expected error when decrypting with wrong key, got nil")
	}
}

func TestEncryptWithoutKey(t *testing.T) {
	svc := &Service{} // encKey is nil
	_, _, err := svc.Encrypt([]byte("test"))
	if err == nil {
		t.Fatal("expected error when encrypting without key, got nil")
	}
}

func TestDecryptWithoutKey(t *testing.T) {
	svc := &Service{} // encKey is nil
	_, err := svc.Decrypt([]byte("ct"), []byte("nonce"))
	if err == nil {
		t.Fatal("expected error when decrypting without key, got nil")
	}
}
