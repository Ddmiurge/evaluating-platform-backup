package sample

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
	"evaluating_platform/internal/repository"
	"evaluating_platform/pkg/storage"
)

// Loader 攻击样本加载器（从 MinIO 明文存储读取）
type Loader struct {
	sampleRepo *repository.AttackSampleRepository
	store      *storage.MinIOClient
}

// NewLoader 创建样本加载器
func NewLoader(
	sampleRepo *repository.AttackSampleRepository,
	store *storage.MinIOClient,
) *Loader {
	return &Loader{sampleRepo: sampleRepo, store: store}
}

// LoadedSamples 加载后的样本集
type LoadedSamples struct {
	Payloads []model.AttackPayload
}

// Close 释放资源
func (ls *LoadedSamples) Close() {
	ls.Payloads = nil
}

// LoadSamples 从 MinIO 明文存储加载样本到内存
func (l *Loader) LoadSamples(ctx context.Context, sampleID uuid.UUID) (*LoadedSamples, error) {
	// 1. 从 DB 查元数据
	sample, err := l.sampleRepo.GetByID(ctx, sampleID)
	if err != nil {
		return nil, fmt.Errorf("get sample: %w", err)
	}

	// 2. 从 MinIO 读取 CSV 明文
	csvData, err := l.store.Download(ctx, sample.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("download csv: %w", err)
	}

	// 3. 解析 CSV
	payloads, err := parsePayloads(csvData)
	if err != nil {
		return nil, fmt.Errorf("parse payloads: %w", err)
	}

	return &LoadedSamples{
		Payloads: payloads,
	}, nil
}

// parsePayloads 将 CSV 数据转换为 AttackPayload 列表
// 支持两种格式：
// 1. 新格式：两列（序号+数据，无表头）
// 2. 旧格式：多列带表头（name, payload, expected_behavior, severity）
func parsePayloads(csvData []byte) ([]model.AttackPayload, error) {
	// 先尝试新的两列格式
	rows, err := ParseTwoColumnCSV(csvData)
	if err == nil {
		payloads := make([]model.AttackPayload, len(rows))
		for i, row := range rows {
			payloads[i] = model.AttackPayload{
				Index: row.Index,
				Data:  row.Data,
			}
		}
		return payloads, nil
	}

	// 回退到旧格式解析（多列带表头）
	return parseLegacyCSV(csvData)
}

// parseLegacyCSV 解析旧格式 CSV（name, payload, expected_behavior, severity 等多列带表头）
func parseLegacyCSV(csvData []byte) ([]model.AttackPayload, error) {
	normalized, err := NormalizeCSVData(csvData)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(bytes.NewReader(normalized))
	reader.FieldsPerRecord = -1 // 允许不定列数
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse legacy CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV file is empty or has no data rows")
	}

	// 找 payload 列（或第二列作为数据列）
	header := records[0]
	payloadCol := -1
	for i, col := range header {
		col = strings.TrimSpace(strings.ToLower(col))
		if col == "payload" || col == "data" {
			payloadCol = i
			break
		}
	}
	if payloadCol < 0 && len(header) >= 2 {
		payloadCol = 1 // 默认第二列
	}
	if payloadCol < 0 {
		return nil, fmt.Errorf("cannot find payload column in legacy CSV")
	}

	var payloads []model.AttackPayload
	for i, record := range records[1:] {
		if payloadCol >= len(record) {
			continue
		}
		data := strings.TrimSpace(record[payloadCol])
		if data == "" {
			continue
		}
		payloads = append(payloads, model.AttackPayload{
			Index: i + 1,
			Data:  data,
		})
	}
	return payloads, nil
}

// Preview 预览样本前 N 条（不返回完整内容）
func (l *Loader) Preview(ctx context.Context, sampleID uuid.UUID, n int) ([]model.AttackPayload, error) {
	loaded, err := l.LoadSamples(ctx, sampleID)
	if err != nil {
		return nil, err
	}
	defer loaded.Close()

	if n > len(loaded.Payloads) {
		n = len(loaded.Payloads)
	}
	// 复制前 N 条（不引用原内存）
	preview := make([]model.AttackPayload, n)
	copy(preview, loaded.Payloads[:n])
	return preview, nil
}
