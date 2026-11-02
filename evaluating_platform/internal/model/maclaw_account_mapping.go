package model

import (
	"time"

	"github.com/google/uuid"
)

type MaclawProvisioningStatus string

const (
	MaclawProvisioningReady  MaclawProvisioningStatus = "ready"
	MaclawProvisioningFailed MaclawProvisioningStatus = "failed"
)

type MaclawAccountMapping struct {
	PlatformUserID       uuid.UUID
	PlatformRole         UserRole
	PlatformEmail        string
	PlatformOrgName      string
	MaclawTenantID       string
	MaclawUserID         string
	MaclawCredentialID   string
	MaclawInstanceID     string
	EncryptedAPIKey      []byte
	EncryptedAPISecret   []byte
	CredentialKeyID      string
	EncryptedAccessToken []byte
	AccessTokenKeyID     string
	AccessTokenExpiresAt *time.Time
	ProvisioningStatus   MaclawProvisioningStatus
	LastError            string
	// P26a · 租户专属引擎实例（G-1/G-3）。空 = 共享实例。
	// token 密文走平台 crypto 基建（与 EncryptedAPIKey 同一把 master key 体系）；
	// 登记是运维动作（compose + 本表），写入路径在 P26c。
	EngineBaseURL         string
	EngineTokenCiphertext []byte
	EngineTokenKeyID      string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}
