package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/model"
	"evaluating_platform/pkg/logger"
)

type TemplateRepository struct {
	pool *pgxpool.Pool
}

func NewTemplateRepository(pool *pgxpool.Pool) *TemplateRepository {
	return &TemplateRepository{pool: pool}
}

const tplSelectCols = ` id, expert_id, upload_batch_id, upload_batch_name, sub_type, name, description, content, variables,
    status, visibility, created_at, updated_at `

// scanTemplate 把一行 templates 映射成领域对象。
//
// 审计 #6：原先是 `_ = json.Unmarshal(varsJSON, &t.Variables)`。variables
// 是提示词模板声明的占位符 —— 它损坏后归零，意味着渲染时不做任何替换，
// 于是**真正发往靶场的攻击文本与被评审的那份悄悄不同**：评测照跑，
// 测的却是另一个东西。因此归类为关键列，fail-closed。
func scanTemplate(row rowScanner) (*model.Template, error) {
	var t model.Template
	var varsJSON []byte
	err := row.Scan(
		&t.ID,
		&t.ExpertID,
		&t.UploadBatchID,
		&t.UploadBatchName,
		&t.SubType,
		&t.Name,
		&t.Description,
		&t.Content,
		&varsJSON,
		&t.Status,
		&t.Visibility,
		&t.CreatedAt,
		&t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := decodeCriticalJSONB("templates", t.ID.String(), "variables", varsJSON, &t.Variables); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TemplateRepository) Create(ctx context.Context, t *model.Template) error {
	varsJSON, err := json.Marshal(t.Variables)
	if err != nil {
		return fmt.Errorf("marshal template variables: %w", err)
	}
	query := `
        INSERT INTO templates
            (id, expert_id, upload_batch_id, upload_batch_name, sub_type, name, description, content, variables, status, visibility)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
    `
	_, err = r.pool.Exec(ctx, query,
		t.ID,
		t.ExpertID,
		t.UploadBatchID,
		t.UploadBatchName,
		t.SubType,
		t.Name,
		t.Description,
		t.Content,
		varsJSON,
		t.Status,
		t.Visibility,
	)
	if err != nil {
		return fmt.Errorf("insert template: %w", err)
	}
	return nil
}

func (r *TemplateRepository) GetByID(ctx context.Context, id uuid.UUID) (*model.Template, error) {
	query := "SELECT" + tplSelectCols + "FROM templates WHERE id = $1"
	t, err := scanTemplate(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("query template: %w", err)
	}
	return t, nil
}

func (r *TemplateRepository) Update(ctx context.Context, t *model.Template) error {
	varsJSON, err := json.Marshal(t.Variables)
	if err != nil {
		return fmt.Errorf("marshal template variables: %w", err)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE templates SET name=$1, description=$2, content=$3, variables=$4,
         sub_type=$5, updated_at=NOW()
         WHERE id=$6 AND expert_id=$7`,
		t.Name, t.Description, t.Content, varsJSON, t.SubType, t.ID, t.ExpertID,
	)
	if err != nil {
		return fmt.Errorf("update template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("template not found or access denied")
	}
	return nil
}

func (r *TemplateRepository) Delete(ctx context.Context, id, expertID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		"DELETE FROM templates WHERE id = $1 AND expert_id = $2", id, expertID,
	)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("template not found or access denied")
	}
	return nil
}

func (r *TemplateRepository) ListByExpert(ctx context.Context, expertID uuid.UUID, subType string, limit, offset int) ([]model.Template, int, error) {
	conds := []string{"expert_id = $1"}
	args := []interface{}{expertID}

	if subType != "" {
		args = append(args, subType)
		conds = append(conds, fmt.Sprintf("sub_type = $%d", len(args)))
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM templates "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count templates: %w", err)
	}

	limitN := len(args) + 1
	offsetN := len(args) + 2
	listArgs := append(args, limit, offset)
	query := fmt.Sprintf("SELECT%sFROM templates %s ORDER BY updated_at DESC LIMIT $%d OFFSET $%d",
		tplSelectCols, where, limitN, offsetN)

	rows, err := r.pool.Query(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()

	templates := make([]model.Template, 0)
	corrupt, err := scanTemplateRows(rows, &templates)
	if err != nil {
		return nil, 0, err
	}
	return templates, total, newPartialCorruptionError("templates", corrupt)
}

func (r *TemplateRepository) ListPublished(ctx context.Context, subType string, limit, offset int) ([]model.Template, int, error) {
	where, args := platformDataPublishedWhereClause("sub_type", subType, 0)

	var total int
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM templates "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count available templates: %w", err)
	}

	limitN := len(args) + 1
	offsetN := len(args) + 2
	listArgs := append(args, limit, offset)
	query := fmt.Sprintf("SELECT%sFROM templates %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
		tplSelectCols, where, limitN, offsetN)

	rows, err := r.pool.Query(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list available templates: %w", err)
	}
	defer rows.Close()

	templates := make([]model.Template, 0)
	corrupt, err := scanTemplateRows(rows, &templates)
	if err != nil {
		return nil, 0, err
	}
	return templates, total, newPartialCorruptionError("templates", corrupt)
}

// scanTemplateRows 逐行扫描模板列表，坏行跳过并记录定位信息。
//
// 为什么列表不因一行坏模板整体失败：与 engine_runs 同理 —— 已经落库的
// 历史脏数据不应让整个模板列表页打不开。降级事实以 PartialCorruptionError
// 上抛，由调用方决定「记日志 + 返回 200」还是「500」。
func scanTemplateRows(rows rowsScanner, out *[]model.Template) ([]*CorruptRowError, error) {
	var corrupt []*CorruptRowError
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				logger.Error("skipping corrupt template row", map[string]interface{}{
					"table":  bad.Table,
					"row":    bad.RowID,
					"column": bad.Field,
					"err":    bad.Err.Error(),
				})
				corrupt = append(corrupt, bad)
				continue
			}
			return corrupt, fmt.Errorf("scan template: %w", err)
		}
		*out = append(*out, *t)
	}
	return corrupt, rows.Err()
}
