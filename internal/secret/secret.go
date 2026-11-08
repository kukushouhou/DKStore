// Package secret 负责凭证加密存储（AES-256-GCM），主密钥保存在数据目录 secret.key（0600）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const keyFile = "secret.key"

// Box 用主密钥加解密数据。
type Box struct {
	aead cipher.AEAD
}

// LoadOrCreate 读取数据目录中的主密钥，不存在时生成。
func LoadOrCreate(dataDir string) (*Box, error) {
	path := filepath.Join(dataDir, keyFile)
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("写入主密钥失败: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("读取主密钥失败: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("主密钥长度不正确: %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal 加密明文，返回 nonce 与密文。
func (b *Box) Seal(plain []byte) (nonce, sealed []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, b.aead.Seal(nil, nonce, plain, nil), nil
}

// Open 解密。
func (b *Box) Open(nonce, sealed []byte) ([]byte, error) {
	return b.aead.Open(nil, nonce, sealed, nil)
}
