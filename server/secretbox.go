package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// apiSecretBox 管理外部接口凭据的对称加密(AES-256-GCM)。
// 密钥文件 data/external_api_secret.key:4 字节版本号 + 32 字节密钥,首次使用自动生成。
// 密文格式: 12 字节随机 nonce + 密文(含 GCM tag)。
type apiSecretBox struct {
	mu  sync.Mutex
	key []byte
}

var errAPISecretBox = errors.New("api secretbox")

func loadOrCreateAPISecretKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "external_api_secret.key")
	if raw, err := os.ReadFile(path); err == nil && len(raw) == 36 {
		return raw[4:], nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("%w: 生成密钥失败: %v", errAPISecretBox, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", errAPISecretBox, err)
	}
	version := []byte{1, 0, 0, 0}
	if err := os.WriteFile(path, append(version, key...), 0o600); err != nil {
		return nil, fmt.Errorf("%w: 写密钥文件失败: %v", errAPISecretBox, err)
	}
	return key, nil
}

func newAPISecretBox(dataDir string) (*apiSecretBox, error) {
	key, err := loadOrCreateAPISecretKey(dataDir)
	if err != nil {
		return nil, err
	}
	return &apiSecretBox{key: key}, nil
}

func (b *apiSecretBox) seal(plaintext []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (b *apiSecretBox) open(ciphertext []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: 密文过短", errAPISecretBox)
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, body, nil)
}

var _ = binary.LittleEndian // 保留:版本号字段预留小端布局
