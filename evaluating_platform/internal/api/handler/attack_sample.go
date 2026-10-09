package handler

import (
	"evaluating_platform/internal/maclaw"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/repository"
	"evaluating_platform/internal/sample"
	"evaluating_platform/pkg/logger"
	"evaluating_platform/pkg/storage"
)

// AttackSampleHandler 攻击样本处理器
type AttackSampleHandler struct {
	sampleRepo *repository.AttackSampleRepository
	manager    *sample.Manager
	loader     *sample.Loader
	store      *storage.MinIOClient
}

// NewAttackSampleHandler 创建攻击样本处理器
func NewAttackSampleHandler(
	sampleRepo *repository.AttackSampleRepository,
	manager *sample.Manager,
	loader *sample.Loader,
	store *storage.MinIOClient,
) *AttackSampleHandler {
	return &AttackSampleHandler{
		sampleRepo: sampleRepo,
		manager:    manager,
		loader:     loader,
		store:      store,
	}
}

// Upload 上传攻击样本 CSV
// POST /api/v1/samples (multipart/form-data)
func (h *AttackSampleHandler) Upload(c *gin.Context) {
	expertID, _ := uuid.Parse(c.GetString("user_id"))
	subType := c.PostForm("sub_type")
	name := c.PostForm("name")
	description := c.PostForm("description")

	if subType == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sub_type and name are required"})
		return
	}
	if h.manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":  "sample_storage_unavailable",
			"error": "sample storage is unavailable, please retry after the storage service is ready",
		})
		return
	}

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	csvData, err := readLimitedUpload(file, maxExpertDataUploadBytes)
	if err != nil {
		writeUploadReadError(c, err)
		return
	}

	s, err := h.manager.Upload(c.Request.Context(), expertID, subType, name, description, csvData)
	if err != nil {
		logger.Error("upload sample failed", map[string]interface{}{
			"expert_id": expertID, "error": err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": maclaw.SafeErrorSummary(err)})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":           s.ID,
		"name":         s.Name,
		"sub_type":     s.SubType,
		"sample_count": s.SampleCount,
		"file_hash":    s.FileHash,
	})
}

// List 列出攻击样本
// GET /api/v1/samples?sub_type=&limit=&offset=
func (h *AttackSampleHandler) List(c *gin.Context) {
	expertID, _ := uuid.Parse(c.GetString("user_id"))
	subType := c.Query("sub_type")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	samples, total, err := h.sampleRepo.ListByExpert(c.Request.Context(), expertID, subType, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": samples, "total": total, "limit": limit, "offset": offset,
	})
}

// Delete 删除攻击样本
// DELETE /api/v1/samples/:id
func (h *AttackSampleHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	expertID, _ := uuid.Parse(c.GetString("user_id"))

	encPath, err := h.sampleRepo.Delete(c.Request.Context(), id, expertID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 删除 MinIO 文件
	if encPath != "" && h.store != nil {
		_ = h.store.Delete(c.Request.Context(), encPath)
	}

	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// Preview 预览样本前 N 条
// GET /api/v1/samples/:id/preview
func (h *AttackSampleHandler) Preview(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 {
		limit = 20
	}
	if h.loader == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":  "sample_storage_unavailable",
			"error": "sample storage is unavailable, please retry after the storage service is ready",
		})
		return
	}

	payloads, err := h.loader.Preview(c.Request.Context(), id, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": maclaw.SafeErrorSummary(err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"preview": payloads, "limit": limit})
}
