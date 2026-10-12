package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func loadDeviceSecretKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read device credential key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("device credential key has an invalid length")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure device credential key: %w", err)
	}
	return key, nil
}

func (m *instanceManager) sealDeviceSecret(secret string) (string, error) {
	if len(m.secretKey) == 0 {
		m.secretKey = make([]byte, 32)
		if _, err := rand.Read(m.secretKey); err != nil {
			return "", err
		}
		if err := writePrivateFile(m.secretKeyPath, m.secretKey); err != nil {
			m.secretKey = nil
			return "", fmt.Errorf("write device credential key: %w", err)
		}
	}
	block, err := aes.NewCipher(m.secretKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(secret), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (m *instanceManager) openDeviceSecret(encoded string) (string, error) {
	if len(m.secretKey) != 32 {
		return "", errors.New("device credential key is missing; restore device-secrets.key from the same EchoManager data directory")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(m.secretKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("encrypted device credential is truncated")
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return os.Chmod(path, 0o600)
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
