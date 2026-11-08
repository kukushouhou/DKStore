package secret

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	box, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	nonce, sealed, err := box.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}

	// 重新加载应读到同一主密钥
	box2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box2.Open(nonce, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, []byte("hunter2")) {
		t.Fatalf("got %q", plain)
	}
}
