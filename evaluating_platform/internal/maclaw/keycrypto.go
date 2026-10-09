package maclaw

// keycrypto.go — KeyStore 加密/解密 JSON 样板共享实现（P2-18）。
// 提取自 provisioner / target_config_service / hub_config_service 三处
// 逐字重复的 CurrentKey→Marshal→Encrypt 与 GetKey→Decrypt→Unmarshal 样板。

import (
	"encoding/json"
	"fmt"

	appcrypto "evaluating_platform/internal/crypto"
)

// encryptJSON 用 KeyStore 当前密钥加密任意 JSON 序列化值。
func encryptJSON(ks *appcrypto.KeyStore, v any, errPrefix string) ([]byte, string, error) {
	keyID, key := ks.CurrentKey()
	data, err := json.Marshal(v)
	if err != nil {
		return nil, "", fmt.Errorf("marshal %s: %w", errPrefix, err)
	}
	encrypted, err := appcrypto.Encrypt(data, key)
	if err != nil {
		return nil, "", fmt.Errorf("encrypt %s: %w", errPrefix, err)
	}
	return encrypted, keyID, nil
}

// decryptJSON 用指定 keyID 解密并反序列化到 out。
func decryptJSON(ks *appcrypto.KeyStore, ciphertext []byte, keyID string, out any, errPrefix string) error {
	key, err := ks.GetKey(keyID)
	if err != nil {
		return fmt.Errorf("load %s key: %w", errPrefix, err)
	}
	plain, err := appcrypto.Decrypt(ciphertext, key)
	if err != nil {
		return fmt.Errorf("decrypt %s: %w", errPrefix, err)
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return fmt.Errorf("decode %s: %w", errPrefix, err)
	}
	return nil
}
