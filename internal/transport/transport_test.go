package transport

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateEd25519KeyPair(t *testing.T) {
	priv, pub, err := GenerateEd25519KeyPair("dkstore-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(priv, "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatalf("unexpected private key format: %s", priv)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, "dkstore-test") {
		t.Fatalf("unexpected authorized key: %s", pub)
	}
	if _, err := ssh.ParsePrivateKey([]byte(priv)); err != nil {
		t.Fatalf("ParsePrivateKey failed: %v", err)
	}
}
