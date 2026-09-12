package cryptox

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// testMaster 是固定测试主秘密（字节 0x00..0x1f）。
func testMaster() []byte {
	m, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		panic(err)
	}
	return m
}

// 冻结测试向量：HKDF info 标签与派生方式一旦变更，已导入数据库的密钥与礼品码
// 将全部失效。此向量用于锁住行为，任何意外改动都会使测试失败。
const (
	wantAccessDigest = "98ae2b95b4319ee0b2aa7afeddd19feb9973b55fbefdc905375a3637011fd85e"
	wantGiftKey      = "8a735b270b468ca52045968aa348434f5bc1260b4560f7800dadaed05d630e86"
)

func TestNewRejectsShortMaster(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatal("New(短主秘密) 应返回错误")
	}
}

func TestAccessKeyDigestVector(t *testing.T) {
	svc, err := New(testMaster())
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(svc.AccessKeyDigest("12345678"))
	if got != wantAccessDigest {
		t.Fatalf("AccessKeyDigest 向量漂移: got %s, want %s", got, wantAccessDigest)
	}
}

func TestAccessKeyDigestDeterministicAndDistinct(t *testing.T) {
	svc, _ := New(testMaster())
	d1 := svc.AccessKeyDigest("12345678")
	d2 := svc.AccessKeyDigest("12345678")
	if !bytes.Equal(d1, d2) {
		t.Fatal("相同输入的摘要必须稳定一致")
	}
	if d := svc.AccessKeyDigest("87654321"); bytes.Equal(d1, d) {
		t.Fatal("不同密钥的摘要不应相同")
	}

	other, _ := New(bytes.Repeat([]byte{0xAB}, 32))
	if d := other.AccessKeyDigest("12345678"); bytes.Equal(d1, d) {
		t.Fatal("不同主秘密的摘要不应相同")
	}
}

func TestGiftKeyVector(t *testing.T) {
	// 直接比对 HKDF 输出，锁定 K_gift_code 的派生行为。
	k, err := derive(testMaster(), infoGiftCode, 32)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(k); got != wantGiftKey {
		t.Fatalf("gift key 向量漂移: got %s, want %s", got, wantGiftKey)
	}
}

func TestGiftCodeRoundTrip(t *testing.T) {
	svc, _ := New(testMaster())

	const plain = "GIFT-CODE-2026-0001"
	ct1, err := svc.EncryptGiftCode(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct1, []byte(plain)) {
		t.Fatal("密文不应包含明文")
	}

	// 每次加密使用独立随机 nonce，同明文密文不同。
	ct2, _ := svc.EncryptGiftCode(plain)
	if bytes.Equal(ct1, ct2) {
		t.Fatal("相同明文的两次加密不应产生相同密文")
	}

	back, err := svc.DecryptGiftCode(ct1)
	if err != nil {
		t.Fatal(err)
	}
	if back != plain {
		t.Fatalf("解密结果 %q, want %q", back, plain)
	}

	// 篡改密文必须失败。
	tampered := bytes.Clone(ct1)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := svc.DecryptGiftCode(tampered); err == nil {
		t.Fatal("篡改后的密文解密应失败")
	}

	// 不同主秘密（错误 app.key）必须失败。
	other, _ := New(bytes.Repeat([]byte{0xAB}, 32))
	if _, err := other.DecryptGiftCode(ct1); err == nil {
		t.Fatal("错误密钥解密应失败")
	}

	// 过短输入必须失败。
	if _, err := svc.DecryptGiftCode([]byte{1, 2, 3}); err == nil {
		t.Fatal("过短密文解密应失败")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	master := testMaster()

	// 形态一：hex 文本（openssl rand -hex 32 风格，含换行）。
	hexPath := filepath.Join(dir, "app.key.hex")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(master)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svcHex, err := Load(hexPath)
	if err != nil {
		t.Fatal(err)
	}

	// 形态二：原始字节。
	rawPath := filepath.Join(dir, "app.key.raw")
	if err := os.WriteFile(rawPath, master, 0o600); err != nil {
		t.Fatal(err)
	}
	svcRaw, err := Load(rawPath)
	if err != nil {
		t.Fatal(err)
	}

	d1 := svcHex.AccessKeyDigest("12345678")
	d2 := svcRaw.AccessKeyDigest("12345678")
	if !bytes.Equal(d1, d2) {
		t.Fatal("hex 与原始字节两种形态应派生出相同密钥材料")
	}

	// 过短文件必须失败。
	shortPath := filepath.Join(dir, "app.key.short")
	if err := os.WriteFile(shortPath, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(shortPath); err == nil {
		t.Fatal("过短 app.key 应报错")
	}

	// 不存在的文件必须失败。
	if _, err := Load(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("缺失 app.key 应报错")
	}
}

func TestSessionToken(t *testing.T) {
	t1, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	t2, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(t1) != 32 {
		t.Fatalf("会话令牌长度 %d, want 32", len(t1))
	}
	if bytes.Equal(t1, t2) {
		t.Fatal("两次生成的会话令牌不应相同")
	}
	if d := SessionTokenDigest(t1); len(d) != 32 {
		t.Fatalf("会话令牌摘要长度 %d, want 32", len(d))
	}
	if bytes.Equal(SessionTokenDigest(t1), SessionTokenDigest(t2)) {
		t.Fatal("不同令牌的摘要不应相同")
	}
}
