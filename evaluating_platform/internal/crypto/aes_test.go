package crypto

// aes_test.go — AES-256-GCM 回归护栏（审计 2026-10-12 P1-4）。
//
// internal/crypto 此前**没有任何测试**，而 AES-GCM + 密钥轮换是整个平台
// 凭据存储的安全关键路径：maclaw 的 target 凭据、hub 配置、资源 payload
// 全部经由这里加密后落库。零测试意味着一次误改（比如把 GCM 换成 CBC、
// 或者 nonce 复用）不会让任何 CI 变红。
//
// 本文件的断言全部围绕「不可逆的破坏」来写：每一项都对应一个真实事故形态。

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// testKey32 返回一个确定性的 32 字节测试密钥。
//
// 刻意用固定值而非随机：测试失败时需要能复现同一把密钥下的行为，
// 且不得依赖 crypto/rand 的不可预测性。
func testKey32(t *testing.T) []byte {
	t.Helper()
	key, err := ParseHexKey(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("ParseHexKey(test key) error = %v", err)
	}
	return key
}

// TestEncryptDecryptRoundTrip 覆盖最基本的往返一致性。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := testKey32(t)
	plaintext := []byte(`{"api_key":"sk-secret-value","target":"https://target.example"}`)

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	got, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round trip mismatch:\n got = %q\nwant = %q", got, plaintext)
	}
}

// TestEncryptDecryptEmptyPlaintext 覆盖空明文：加密必须成功，
// 且解密后必须还原成空（而不是 nil 或报错）。
func TestEncryptDecryptEmptyPlaintext(t *testing.T) {
	key := testKey32(t)

	ciphertext, err := Encrypt([]byte{}, key)
	if err != nil {
		t.Fatalf("Encrypt(empty) error = %v", err)
	}
	got, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt(empty) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Decrypt(empty) len = %d, want 0", len(got))
	}
}

// TestEncryptDecryptLargePlaintext 覆盖跨 AES-GCM 常规块大小的载荷，
// 防止长度/分块处理被改坏。
func TestEncryptDecryptLargePlaintext(t *testing.T) {
	key := testKey32(t)
	plaintext := bytes.Repeat([]byte("A"), 64*1024) // 64 KiB

	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt(large) error = %v", err)
	}
	got, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decrypt(large) error = %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("large round trip mismatch: got %d bytes, want %d", len(got), len(plaintext))
	}
}

// TestEncryptRejectsWrongKeyLength 覆盖「错误长度密钥必须报错」。
//
// 这是 fail-closed 的关键：若这段错误被忽略（例如写成 `_`），后续代码会
// 带着 nil key 继续跑 —— aes.NewCipher 返回的 block 为 nil，随后的
// cipher.NewGCM(nil) 才会panic，或在某些改写下悄悄退化成弱加密。
//
// 注意「错误长度」的准确边界：Go 的 crypto/aes 接受 16 / 24 / 32 字节
// （AES-128/192/256），所以 16 和 24 **不是** 错误。平台要求的是
// AES-256，这一层由 ParseHexKey 强制（见 TestParseHexKey）；
// Encrypt/Decrypt 本身按标准库语义只拒绝「非 16/24/32」的输入。
// 断言必须贴合真实契约，否则测试会逼着实现去拒绝合法密钥。
func TestEncryptRejectsWrongKeyLength(t *testing.T) {
	// crypto/aes 不接受的长度。
	for _, size := range []int{0, 1, 8, 15, 17, 23, 25, 31, 33, 64} {
		key := bytes.Repeat([]byte{0x42}, size)
		if _, err := Encrypt([]byte("payload"), key); err == nil {
			t.Fatalf("Encrypt with %d-byte key: want error, got nil", size)
		}
		if _, err := Decrypt([]byte("0123456789012345678901234567890123456789"), key); err == nil {
			t.Fatalf("Decrypt with %d-byte key: want error, got nil", size)
		}
	}
}

// TestEncryptAcceptsStandardAESSizes 锁定「16/24/32 都合法」这一事实。
//
// 与上一个测试互为对照：那条测「非法长度被拒」，这条测「合法长度不被
// 误拒」。两条同时存在，才能区分「正确实现」与「把所有非 32 长度都
// 拒掉的过度收紧实现」—— 后者会让运营在换密钥时莫名失败。
func TestEncryptAcceptsStandardAESSizes(t *testing.T) {
	for _, size := range []int{16, 24, 32} {
		key := bytes.Repeat([]byte{0x42}, size)
		ciphertext, err := Encrypt([]byte("payload"), key)
		if err != nil {
			t.Fatalf("Encrypt with %d-byte key error = %v", size, err)
		}
		got, err := Decrypt(ciphertext, key)
		if err != nil {
			t.Fatalf("Decrypt with %d-byte key error = %v", size, err)
		}
		if !bytes.Equal(got, []byte("payload")) {
			t.Fatalf("%d-byte key round trip = %q", size, got)
		}
	}
}

// TestDecryptRejectsWrongKey 覆盖「用另一把合法长度的密钥解密」必须失败。
//
// 与上面的长度错误不同：这里 key 长度合法（aes.NewCipher 不报错），
// 必须靠 GCM 的认证标签发现「这不是我加密的」。若认证被摘掉，
// 攻击者（或仅仅是运维误操作）就能用任意 key 解出凭据。
func TestDecryptRejectsWrongKey(t *testing.T) {
	key := testKey32(t)
	otherKey, err := ParseHexKey(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatalf("ParseHexKey(other) error = %v", err)
	}

	ciphertext, err := Encrypt([]byte("top-secret-credential"), key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if _, err := Decrypt(ciphertext, otherKey); err == nil {
		t.Fatal("Decrypt with a different key: want error, got nil")
	}
}

// TestDecryptRejectsShortCiphertext 覆盖「短密文必须报错，且不得 panic」。
//
// 反证要点：ciphertext 被截断到只有 nonce、或干脆是空串时，
// 曾经的写法是直接 ciphertext[:nonceSize] 切片后送进 gcm.Open。
// 一旦 nonceSize 判断被写错（例如写成 < 而不是 <=），空串就会切出
// 越界或让 Open 拿到垃圾。本测试逐个截断长度点做穷举，任何 panic 都会
// 让测试直接失败（Go 的 panic 会终止测试进程，不是被吞掉）。
func TestDecryptRejectsShortCiphertext(t *testing.T) {
	key := testKey32(t)
	ciphertext, err := Encrypt([]byte("a payload long enough to be truncated many ways"), key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	for _, n := range []int{0, 1, 5, 11, 12, 13, 15, 16, 20, len(ciphertext) - 1} {
		if n < 0 || n > len(ciphertext) {
			continue
		}
		truncated := ciphertext[:n]
		got, err := Decrypt(truncated, key)
		if err == nil {
			t.Fatalf("Decrypt(truncated to %d bytes): want error, got plaintext %q", n, got)
		}
		if got != nil {
			t.Fatalf("Decrypt(truncated to %d bytes): error path must not return plaintext, got %q", n, got)
		}
	}
}

// TestDecryptRejectsTamperedCiphertext 覆盖「密文被改一位必须报错」。
//
// GCM 的 AEAD 性质：密文/认证标签任一位被篡改，Open 都会失败。
// 若有人把 AAD 校验或 tag 校验摘掉，这条就会失败 —— 而它正是
// 「攻击者改了落库密文」这条威胁的唯一防线。
func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	key := testKey32(t)
	plaintext := []byte("credential-blob-that-must-not-be-tamperable")
	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	// 逐位翻转密文尾部（覆盖 nonce 与 认证标签区）。
	for i := 0; i < len(ciphertext); i++ {
		tampered := make([]byte, len(ciphertext))
		copy(tampered, ciphertext)
		tampered[i] ^= 0x01
		got, err := Decrypt(tampered, key)
		if err == nil {
			t.Fatalf("Decrypt(tampered byte %d): want error, got %q", i, got)
		}
		if got != nil {
			t.Fatalf("Decrypt(tampered byte %d): must not return plaintext, got %q", i, got)
		}
	}
}

// TestEncryptUsesFreshNonce 覆盖 nonce 随机性。
//
// 断言是「两次加密同一明文的密文不同」。AES-GCM 下nonce 重用是致命的：
// 复用 nonce 会泄露明文 XOR、暴露 GHASH 认证密钥，从而**彻底击穿**
// 机密性与完整性。所以这不是「概率性断言」—— 换成固定 nonce 的实现
// 必然在这里失败。
func TestEncryptUsesFreshNonce(t *testing.T) {
	key := testKey32(t)
	plaintext := []byte("identical plaintext across many encryptions")

	const rounds = 64
	seen := make(map[string]struct{}, rounds)
	for i := 0; i < rounds; i++ {
		ciphertext, err := Encrypt(plaintext, key)
		if err != nil {
			t.Fatalf("Encrypt(round %d) error = %v", i, err)
		}
		// 记录 nonce（密文前 12 字节 = GCM 标准 nonce 长度）。
		nonce := hex.EncodeToString(ciphertext[:12])
		if _, dup := seen[nonce]; dup {
			t.Fatalf("nonce reused at round %d: %s", i, nonce)
		}
		seen[nonce] = struct{}{}

		// 密文整体也必须各不相同（不同 nonce ⇒ 不同 keystream）。
		full := hex.EncodeToString(ciphertext)
		if _, dup := seen["full:"+full]; dup {
			t.Fatalf("identical ciphertext at round %d", i)
		}
		seen["full:"+full] = struct{}{}
	}
}

// TestEncryptProducesDecryptableCiphertextAcrossRounds 把 nonce 随机性与
// 往返一致性串起来：随机 nonce 之后每一条密文都必须仍能被解回原文。
func TestEncryptProducesDecryptableCiphertextAcrossRounds(t *testing.T) {
	key := testKey32(t)
	plaintext := []byte("payload that survives nonce randomisation")

	for i := 0; i < 32; i++ {
		ciphertext, err := Encrypt(plaintext, key)
		if err != nil {
			t.Fatalf("Encrypt(round %d) error = %v", i, err)
		}
		got, err := Decrypt(ciphertext, key)
		if err != nil {
			t.Fatalf("Decrypt(round %d) error = %v", i, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round %d: got %q, want %q", i, got, plaintext)
		}
	}
}

// TestParseHexKey 覆盖密钥解析的边界。
func TestParseHexKey(t *testing.T) {
	t.Run("valid 32 bytes", func(t *testing.T) {
		key, err := ParseHexKey(strings.Repeat("0f", 32))
		if err != nil {
			t.Fatalf("ParseHexKey(valid) error = %v", err)
		}
		if len(key) != 32 {
			t.Fatalf("len(key) = %d, want 32", len(key))
		}
	})

	t.Run("wrong length", func(t *testing.T) {
		for _, bad := range []string{
			"",
			"abcd",
			strings.Repeat("ab", 31), // 31 bytes
			strings.Repeat("ab", 33), // 33 bytes
		} {
			if _, err := ParseHexKey(bad); err == nil {
				t.Fatalf("ParseHexKey(%.16q...): want error, got nil", bad)
			}
		}
	})

	t.Run("not hex", func(t *testing.T) {
		if _, err := ParseHexKey(strings.Repeat("zz", 32)); err == nil {
			t.Fatal("ParseHexKey(non-hex): want error, got nil")
		}
	})
}

// TestSHA256Hex 覆盖哈希助手：必须与已知向量一致。
//
// 用标准向量而非「自己算一遍对比」，否则哈希实现被改坏时测试会跟着改。
func TestSHA256Hex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, c := range cases {
		if got := SHA256Hex([]byte(c.in)); got != c.want {
			t.Fatalf("SHA256Hex(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
