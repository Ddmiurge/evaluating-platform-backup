package model

import (
	"time"

	"github.com/google/uuid"
)

// AttackSample 攻击样本（明文存储到 MinIO，DB 只存元数据）
type AttackSample struct {
	ID          uuid.UUID `json:"id" db:"id"`
	ExpertID    uuid.UUID `json:"expert_id" db:"expert_id"`
	SubType     string    `json:"sub_type" db:"sub_type"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	StoragePath string    `json:"-" db:"storage_path"`      // MinIO 明文路径，不暴露给前端
	FileHash    string    `json:"file_hash" db:"file_hash"` // SHA-256 完整性校验
	SampleCount int       `json:"sample_count" db:"sample_count"`
	FileSize    int64     `json:"file_size" db:"file_size"`
	Status      string    `json:"status" db:"status"`
	Visibility  string    `json:"visibility" db:"visibility"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

// AttackPayload 从 CSV 解析出的单条攻击载荷（两列格式：序号+数据）
type AttackPayload struct {
	Index int    `json:"index"`
	Data  string `json:"data"`
}
