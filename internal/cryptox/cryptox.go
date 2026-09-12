// Package cryptox 实现 WeddingGift 的最小加密边界（技术架构 §10、WG-302）。
//
// 单一应用秘密 app.key 经 HKDF 派生两个独立用途：
//   - K_access_lookup：8 位卡片密钥的 HMAC-SHA256 摘要（明文密钥不入库，
//     且 8 位数字空间小，必须用带服务端秘密的 HMAC，不能离线穷举）；
//   - K_gift_code：礼品激活码的 AES-256-GCM 加密（每条独立随机 nonce）。
//
// 会话使用 256 bit 随机令牌，数据库只存其 SHA-256 摘要。
// 不建设多 keyring、在线轮换或恢复纪元（ADR-009）。
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// minMasterKeyLen 是 app.key 派生前要求的最小熵长度（字节）。
const minMasterKeyLen = 32

// HKDF info 标签：两个派生用途彼此隔离，任何变更都会使既有数据不可解密。
const (
	infoAccessLookup = "wedding/access-key-lookup-v1"
	infoGiftCode     = "wedding/gift-code-v1"
)

// Service 持有派生后的密钥材料。构造后 master 可被调用方清零。
type Service struct {
	accessLookupKey []byte
	giftAEAD        cipher.AEAD
}

// derive 用 HKDF-SHA256 从主秘密派生指定用途的密钥材料。
// info 标签与派生方式冻结：任何变更都会使既有数据库的密钥与礼品码失效。
func derive(master []byte, info string, keyLen int) ([]byte, error) {
	return hkdf.Key(sha256.New, master, nil, info, keyLen)
}

// New 从主秘密派生全部密钥材料。master 至少 32 字节。
func New(master []byte) (*Service, error) {
	if len(master) < minMasterKeyLen {
		return nil, fmt.Errorf("app.key 长度不足：需要至少 %d 字节，当前 %d 字节", minMasterKeyLen, len(master))
	}

	accessKey, err := derive(master, infoAccessLookup, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("派生密钥摘要用途失败: %w", err)
	}
	giftKey, err := derive(master, infoGiftCode, 32)
	if err != nil {
		return nil, fmt.Errorf("派生礼品码加密用途失败: %w", err)
	}
	block, err := aes.NewCipher(giftKey)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("初始化 AES-GCM 失败: %w", err)
	}
	for i := range giftKey {
		giftKey[i] = 0
	}
	return &Service{accessLookupKey: accessKey, giftAEAD: aead}, nil
}

// Load 从文件读取主秘密并派生密钥材料。
//
// 文件内容支持两种形态（生成方式见 ops 文档）：
//   - 十六进制文本（如 `openssl rand -hex 32` 输出，允许首尾空白），解码后 ≥32 字节；
//   - 原始随机字节（如 `head -c 32 /dev/urandom`），文件本身 ≥32 字节。
func Load(path string) (*Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 app.key 失败: %w", err)
	}
	master := raw
	if trimmed := strings.TrimSpace(string(raw)); isHex(trimmed) {
		if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) >= minMasterKeyLen {
			master = decoded
		}
	}
	if len(master) < minMasterKeyLen {
		return nil, fmt.Errorf("app.key 长度不足：需要至少 %d 字节", minMasterKeyLen)
	}
	svc, err := New(master)
	for i := range master {
		master[i] = 0
	}
	for i := range raw {
		raw[i] = 0
	}
	return svc, err
}

// isHex 报告字符串是否整体为偶数长度的十六进制。
func isHex(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// AccessKeyDigest 计算 8 位卡片密钥的 HMAC-SHA256 摘要（32 字节）。
// 调用方需先按 domain.IsValidAccessKeyCode 校验格式。
func (s *Service) AccessKeyDigest(rawKey string) []byte {
	mac := hmac.New(sha256.New, s.accessLookupKey)
	mac.Write([]byte(rawKey))
	return mac.Sum(nil)
}

// EncryptGiftCode 用 AES-256-GCM 加密礼品激活码，输出 nonce||ciphertext。
func (s *Service) EncryptGiftCode(plaintext string) ([]byte, error) {
	nonce := make([]byte, s.giftAEAD.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败: %w", err)
	}
	return s.giftAEAD.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// DecryptGiftCode 解密 EncryptGiftCode 的输出。密文被篡改或密钥错误时返回错误。
func (s *Service) DecryptGiftCode(blob []byte) (string, error) {
	ns := s.giftAEAD.NonceSize()
	if len(blob) < ns+s.giftAEAD.Overhead() {
		return "", errors.New("礼品码密文长度非法")
	}
	plain, err := s.giftAEAD.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", errors.New("礼品码解密失败")
	}
	return string(plain), nil
}

// sessionTokenLen 是会话令牌长度（256 bit）。
const sessionTokenLen = 32

// NewSessionToken 生成 256 bit 随机会话令牌。
func NewSessionToken() ([]byte, error) {
	token := make([]byte, sessionTokenLen)
	if _, err := io.ReadFull(rand.Reader, token); err != nil {
		return nil, fmt.Errorf("生成会话令牌失败: %w", err)
	}
	return token, nil
}

// SessionTokenDigest 计算会话令牌的 SHA-256 摘要（数据库只存摘要）。
func SessionTokenDigest(token []byte) []byte {
	sum := sha256.Sum256(token)
	return sum[:]
}
