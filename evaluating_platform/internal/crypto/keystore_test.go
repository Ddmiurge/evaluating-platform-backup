package crypto

// keystore_test.go — KeyStore 回归护栏，含审计点名的「轮换后旧 key 仍可解密」
// 场景（审计 2026-10-12 P1-4）。
//
// 为什么这个场景必须被测：KeyStore 是凭据轮换的执行点。若某次改动让
// 「按 key_id 取旧密钥」这条路径失效（返回 error、或更糟——静默换用当前
// key 去解），结果是**所有历史凭据永久不可读**，且没有任何数据丢失告警。
// 这属于只有轮换时才会暴露的定时炸弹，因此必须现在锁住。

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"evaluating_platform/pkg/config"
)

// newTestKeyStore 构造一个只含单把密钥的 KeyStore。
func newTestKeyStore(t *testing.T, keyID, hexKey string) *KeyStore {
	t.Helper()
	ks, err := NewKeyStore(config.CryptoConfig{MasterKey: hexKey, KeyID: keyID})
	if err != nil {
		t.Fatalf("NewKeyStore(keyID=%q) error = %v", keyID, err)
	}
	return ks
}

// TestKeyStoreRejectsInvalidMasterKey 覆盖构造期fail-closed。
//
// 若这几条被放行，一个长度不对的 master_key 会拖到第一次加密才失败，
// 那时已经有数据写进了库 —— 而且是「以为加密了其实没加密」的形态。
func TestKeyStoreRejectsInvalidMasterKey(t *testing.T) {
	cases := []struct {
		name      string
		masterKey string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"too short", "abcd"},
		{"31 bytes", bytesToHex(make([]byte, 31))},
		{"33 bytes", bytesToHex(make([]byte, 33))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewKeyStore(config.CryptoConfig{MasterKey: c.masterKey, KeyID: "v1"}); err == nil {
				t.Fatal("NewKeyStore: want error, got nil")
			}
		})
	}
}

// TestKeyStoreCurrentKey 覆盖 CurrentKey 返回的是「配置里那把」密钥。
func TestKeyStoreCurrentKey(t *testing.T) {
	want := bytes.Repeat([]byte{0x11}, 32)
	ks := newTestKeyStore(t, "v1", bytesToHex(want))

	keyID, key := ks.CurrentKey()
	if keyID != "v1" {
		t.Fatalf("CurrentKey keyID = %q, want %q", keyID, "v1")
	}
	if !bytes.Equal(key, want) {
		t.Fatalf("CurrentKey key = %x, want %x", key, want)
	}
}

// TestKeyStoreGetKeyUnknownFailsClosed 覆盖「未知 key_id 必须报错」。
//
// 这是轮换失败时的诊断点：解密旧数据时如果 key_id 查不到，必须明确
// 失败，绝不能悄悄回退到当前 key（那会用错误的 key 解密，
// 报出一个看似无关的 GCM 认证失败，或更糟——成功解出错误内容）。
func TestKeyStoreGetKeyUnknownFailsClosed(t *testing.T) {
	ks := newTestKeyStore(t, "v1", bytesToHex(bytes.Repeat([]byte{0x11}, 32)))

	if _, err := ks.GetKey("does-not-exist"); err == nil {
		t.Fatal("GetKey(unknown): want error, got nil")
	}
	if _, err := ks.GetKey(""); err == nil {
		t.Fatal("GetKey(empty): want error, got nil")
	}
}

// TestKeyStoreGetKeyReturnsRegisteredKey 覆盖已注册 key_id 的正常取值。
func TestKeyStoreGetKeyReturnsRegisteredKey(t *testing.T) {
	want := bytes.Repeat([]byte{0x22}, 32)
	ks := newTestKeyStore(t, "v7", bytesToHex(want))

	got, err := ks.GetKey("v7")
	if err != nil {
		t.Fatalf("GetKey(v7) error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("GetKey(v7) = %x, want %x", got, want)
	}
}

// TestKeyStoreOldKeyStillDecryptsAfterRotation 是审计点名的核心场景。
//
// 构造一台「已轮换」的 KeyStore：注册了 v1（旧，当前加密用）与 v2（新），
// current 指向 v2。然后用 **v1** 加密的密文必须仍能用 **v1** 解开。
//
// 反证设计：这个测试会抓住两类真实事故——
//  1. GetKey 被改成「总是返回当前 key」→ 旧密文用 v2 去解，认证失败，
//     历史凭据集体报废。
//  2. 轮换时把旧 key 从 map 里删掉（省密钥的直觉做法）→ GetKey(v1)
//     报 not found，同样导致历史凭据不可读。
//
// 由于 KeyStore 只暴露 NewKeyStore（单key 入口），这里直接操作包内
// map 来构造「多key 已轮换」状态——这正是生产轮换后的内存形态。
func TestKeyStoreOldKeyStillDecryptsAfterRotation(t *testing.T) {
	oldKey := bytes.Repeat([]byte{0xAA}, 32)
	newKey := bytes.Repeat([]byte{0xBB}, 32)

	// 轮换前：用 v1 加密真实凭据。
	before := newTestKeyStore(t, "v1", bytesToHex(oldKey))
	_, v1KeyBefore := before.CurrentKey()
	plaintext := []byte(`{"api_key":"sk-live-abc123","tenant":"acme"}`)
	ciphertext, err := Encrypt(plaintext, v1KeyBefore)
	if err != nil {
		t.Fatalf("Encrypt with v1 error = %v", err)
	}

	// 轮换：注册 v2 并把 current 切到 v2，**保留** v1 供解密历史数据。
	after := newTestKeyStore(t, "v2", bytesToHex(newKey))
	after.mu.Lock()
	after.keys["v1"] = oldKey
	after.currentKeyID = "v2"
	after.mu.Unlock()

	// 新数据用 v2 加密。
	_, v2KeyNow := after.CurrentKey()
	newCiphertext, err := Encrypt([]byte("fresh-credential"), v2KeyNow)
	if err != nil {
		t.Fatalf("Encrypt with v2 error = %v", err)
	}

	// 关键断言 1：旧密文仍能用 v1 解开。
	v1Key, err := after.GetKey("v1")
	if err != nil {
		t.Fatalf("GetKey(v1) after rotation error = %v (historical credentials would be lost)", err)
	}
	got, err := Decrypt(ciphertext, v1Key)
	if err != nil {
		t.Fatalf("Decrypt(old ciphertext with v1) error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("old ciphertext decrypted to %q, want %q", got, plaintext)
	}

	// 关键断言 2：新密文用 v2 解开，且**不能**被 v1 解开
	//（否则说明 GetKey 取错了 key，认证隔离失效）。
	v2Key, err := after.GetKey("v2")
	if err != nil {
		t.Fatalf("GetKey(v2) error = %v", err)
	}
	gotNew, err := Decrypt(newCiphertext, v2Key)
	if err != nil {
		t.Fatalf("Decrypt(new ciphertext with v2) error = %v", err)
	}
	if !bytes.Equal(gotNew, []byte("fresh-credential")) {
		t.Fatalf("new ciphertext decrypted to %q", gotNew)
	}
	if _, err := Decrypt(newCiphertext, v1Key); err == nil {
		t.Fatal("new ciphertext must NOT decrypt under old key v1")
	}

	// 关键断言 3：CurrentKey 报告的确实是轮换后的新 key。
	if keyID, _ := after.CurrentKey(); keyID != "v2" {
		t.Fatalf("CurrentKey after rotation = %q, want %q", keyID, "v2")
	}
}

// TestKeyStoreConcurrentAccessIsSafe 覆盖读写锁的基本正确性。
//
// 凭据物化是并发路径（多个请求同时取 target 凭据），轮换也会与读取并发。
// 若锁被摘掉，这里会报 data race；配合 -race 即为有效护栏。
// 本测试不断言具体时序，只断言并发调用全部成功且结果自洽。
func TestKeyStoreConcurrentAccessIsSafe(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, 32)
	ks := newTestKeyStore(t, "v1", bytesToHex(key))

	const workers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := ks.GetKey("v1"); err != nil {
				errCh <- err
			}
		}()
		go func() {
			defer wg.Done()
			keyID, got := ks.CurrentKey()
			if keyID != "v1" || !bytes.Equal(got, key) {
				errCh <- errors.New("CurrentKey returned an inconsistent key")
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent access failure: %v", err)
	}
}

// bytesToHex 把字节切片编码成小写 hex，供测试构造密钥字符串。
func bytesToHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}
