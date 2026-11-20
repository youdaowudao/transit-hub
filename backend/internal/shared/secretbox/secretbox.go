// Package secretbox changes only the stored representation of management credentials.
// A nil Box preserves legacy plaintext writes but refuses encrypted reads.
package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const Prefix = "enc:v1:"

var (
	ErrInvalidKey    = errors.New("读取管理凭据密钥失败：CREDENTIAL_ENCRYPTION_KEY 必须是无空白的 base64 编码且解码后恰好 32 字节；请修正配置后重启")
	ErrRandom        = errors.New("生成管理凭据加密随机数失败；本次未保存，请检查系统随机数服务后重试")
	ErrNoKey         = errors.New("已保存的管理凭据需要原加密密钥，当前未配置；请恢复原 CREDENTIAL_ENCRYPTION_KEY 后重试，不要覆盖已有数据")
	ErrUndecryptable = errors.New("管理凭据无法用当前密钥解密，可能是密钥不匹配、内容损坏或保存位置不一致；请核对原密钥和备份后重试，不要覆盖已有数据")
)

type Box struct{ aead cipher.AEAD }

// FromConfig never creates key files or derives keys for unrelated features.
func FromConfig(value string) (*Box, error) {
	if value == "" {
		return nil, nil
	}
	if strings.ContainsAny(value, "\r\n\t ") {
		return nil, ErrInvalidKey
	}
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, ErrInvalidKey
	}
	return New(key)
}

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("创建管理凭据加密器失败：密钥必须恰好 32 字节")
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

func Optional(boxes []*Box) *Box {
	if len(boxes) == 0 {
		return nil
	}
	return boxes[0]
}

// AAD uses a structured tuple so separators in identifiers cannot collide.
func AAD(location string, parts ...string) string {
	encoded, _ := json.Marshal(append([]string{location}, parts...))
	return string(encoded)
}

func IsSealed(value string) bool { return strings.HasPrefix(value, Prefix) }
func IsSealedJSON(value []byte) bool {
	var text string
	return json.Unmarshal(value, &text) == nil && IsSealed(text)
}

func (b *Box) Seal(plain []byte, aad string) (string, error) {
	if b == nil {
		return string(plain), nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrRandom
	}
	return Prefix + base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, plain, []byte(aad))), nil
}

func (b *Box) Open(stored, aad string) ([]byte, error) {
	if !IsSealed(stored) {
		return []byte(stored), nil
	}
	if b == nil {
		return nil, ErrNoKey
	}
	sealed, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil || len(sealed) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, ErrUndecryptable
	}
	plain, err := b.aead.Open(nil, sealed[:b.aead.NonceSize()], sealed[b.aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, ErrUndecryptable
	}
	return plain, nil
}

func (b *Box) SealJSON(plain []byte, aad string) ([]byte, error) {
	if b == nil || len(plain) == 0 {
		return plain, nil
	}
	sealed, err := b.Seal(plain, aad)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealed)
}

func (b *Box) OpenJSON(stored []byte, aad string) ([]byte, error) {
	trimmed := bytes.TrimSpace(stored)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return stored, nil
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return nil, errors.New("读取管理凭据失败：保存的 JSON 字符串损坏；请核对备份后重试")
	}
	if !IsSealed(text) {
		return stored, nil
	}
	return b.Open(text, aad)
}

// At adds only storage identifiers to an already sanitized codec error.
func At(location, user, workspace, object string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("处理管理凭据失败（位置 %s，用户 %s，工作区 %s，对象 %s）：%w", location, user, workspace, object, err)
}
