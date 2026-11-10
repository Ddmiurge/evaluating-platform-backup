// Embedded-plugin state machine (P28, docs/architecture/embedded-plugin-track.md §4).
//
// Every admin/tool action is a method with its ALLOWED FROM-states compiled in:
// an illegal transition is impossible by construction (the repository's
// `state = ANY(expected)` is then only the optimistic-concurrency guard against
// two admins racing the same package — 409 ErrEmbeddedStateConflict, never a
// silent double-move).
//
//   pending_initial_review → rejected | approved_for_build
//   rejected               → pending_initial_review          (re-upload)
//   approved_for_build     → build_requested | rejected
//   build_requested        → build_failed | testing          (tool)
//   build_failed           → build_requested | rejected
//   testing                → approved | pending_initial_review (re-upload)
//   approved               → disabled | remove_requested | pending_initial_review
//   disabled               → approved | remove_requested
//   remove_requested       → approved | removed              (tool)
//   removed                → (terminal)
//
// ANY re-upload (rule 1/3): version+1, back to pending_initial_review, full
// gates re-run — except from `removed`, which is terminal (a removed id needs
// a new id; reviving is an admin decision, not an expert upload).

package maclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	EmbeddedStatePendingInitialReview = "pending_initial_review"
	EmbeddedStateRejected             = "rejected"
	EmbeddedStateApprovedForBuild     = "approved_for_build"
	EmbeddedStateBuildRequested       = "build_requested"
	EmbeddedStateBuildFailed          = "build_failed"
	EmbeddedStateTesting              = "testing"
	EmbeddedStateApproved             = "approved"
	EmbeddedStateDisabled             = "disabled"
	EmbeddedStateRemoveRequested      = "remove_requested"
	EmbeddedStateRemoved              = "removed"
)

// EmbeddedStateLabel maps states to the portal wording (single source for FE/BE).
func EmbeddedStateLabel(state string) string {
	switch state {
	case EmbeddedStatePendingInitialReview:
		return "待初审"
	case EmbeddedStateRejected:
		return "已驳回"
	case EmbeddedStateApprovedForBuild:
		return "已批准·待构建"
	case EmbeddedStateBuildRequested:
		return "已请求构建"
	case EmbeddedStateBuildFailed:
		return "构建失败"
	case EmbeddedStateTesting:
		return "测试中"
	case EmbeddedStateApproved:
		return "已通过"
	case EmbeddedStateDisabled:
		return "已停用"
	case EmbeddedStateRemoveRequested:
		return "标记移除"
	case EmbeddedStateRemoved:
		return "已移除"
	default:
		return state
	}
}

// EmbeddedPluginStateRow / EmbeddedPluginEvent live HERE (not in repository)
// because the repository already imports maclaw — maclaw must not import it
// back (the import-cycle rule that ModelDefaultStore/EngineInstanceStore
// follow: interface in maclaw, concrete store in repository).
type EmbeddedPluginStateRow struct {
	OwnerUserID      uuid.UUID
	ResourceID       string
	PackageID        string
	Name             string
	Summary          string
	State            string
	Version          string
	AutoGate         []byte // JSON of EmbeddedAutoGateSummary
	ReviewReason     string
	ReviewerUserID   *uuid.UUID
	BuildRequestedBy *uuid.UUID
	BuildRequestedAt *time.Time
	FinalApprovedBy  *uuid.UUID
	FinalApprovedAt  *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// EmbeddedPluginEvent is one immutable audit line (rule 1: history readable).
type EmbeddedPluginEvent struct {
	Version   string
	Event     string
	Actor     *uuid.UUID
	Reason    string
	CreatedAt time.Time
}

// EmbeddedPluginStore is the persistence surface the lifecycle needs.
type EmbeddedPluginStore interface {
	GetByPackageID(context.Context, string) (*EmbeddedPluginStateRow, error)
	Insert(context.Context, EmbeddedPluginStateRow) error
	Transition(ctx context.Context, packageID string, expected []string, to string, patch map[string]any, event string, actor *uuid.UUID, reason string) error
	IsTester(context.Context, uuid.UUID) (bool, error)
}

type EmbeddedPluginLifecycle struct {
	repo EmbeddedPluginStore
}

func NewEmbeddedPluginLifecycle(repo EmbeddedPluginStore) *EmbeddedPluginLifecycle {
	return &EmbeddedPluginLifecycle{repo: repo}
}

func gateJSON(gate EmbeddedAutoGateSummary) []byte {
	raw, err := json.Marshal(gate)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// RecordUpload is called after the store exit accepted the package: create the
// pending row (first upload) or move an existing one back to pending with
// version+1 (rule 1/3). `removed` is terminal.
func (l *EmbeddedPluginLifecycle) RecordUpload(ctx context.Context, owner uuid.UUID, resourceID string, m *EmbeddedManifest, gate EmbeddedAutoGateSummary, reupload bool) error {
	if l == nil || l.repo == nil {
		return nil
	}
	existing, err := l.repo.GetByPackageID(ctx, m.ID)
	if err != nil {
		return fmt.Errorf("read embedded state: %w", err)
	}
	if existing == nil {
		if reupload {
			// The caller believes this is a re-upload but no row exists — the
			// expert edited the id. Treat as first upload (the id-conflict check
			// in the store exit already ruled out collisions with live packages).
			reupload = false
		}
		return l.repo.Insert(ctx, EmbeddedPluginStateRow{
			OwnerUserID: owner,
			ResourceID:  resourceID,
			PackageID:   m.ID,
			Name:        m.Name,
			Summary:     m.Summary,
			State:       EmbeddedStatePendingInitialReview,
			Version:     "1",
			AutoGate:    gateJSON(gate),
		})
	}
	if existing.State == EmbeddedStateRemoved {
		return &EmbeddedPluginValidationError{Code: "embedded_removed_terminal",
			Message: fmt.Sprintf("包 %s 已移除（终态）：如需继续使用请以新 id 发布，或联系管理员解除移除。", m.ID)}
	}
	nextVersion := bumpEmbeddedVersion(existing.Version)
	return l.repo.Transition(ctx, m.ID, []string{existing.State}, EmbeddedStatePendingInitialReview,
		map[string]any{
			"resource_id": resourceID,
			"version":     nextVersion,
			"auto_gate":   gateJSON(gate),
			"name":        m.Name,
			"summary":     m.Summary,
		},
		"reuploaded", &owner, fmt.Sprintf("版本 %s → %s", existing.Version, nextVersion))
}

// RecordRejection stores the auto-gate verdict for an upload that never got a
// resource row (first upload) or moves an existing package to rejected
// (re-upload that failed the gates). The invalid payload is never persisted.
func (l *EmbeddedPluginLifecycle) RecordRejection(ctx context.Context, owner uuid.UUID, packageID, version, name string, gate EmbeddedAutoGateSummary) error {
	if l == nil || l.repo == nil {
		return nil
	}
	existing, err := l.repo.GetByPackageID(ctx, packageID)
	if err != nil {
		return fmt.Errorf("read embedded state: %w", err)
	}
	if existing == nil {
		return l.repo.Insert(ctx, EmbeddedPluginStateRow{
			OwnerUserID: owner,
			ResourceID:  "",
			PackageID:   packageID,
			Name:        name,
			State:       EmbeddedStateRejected,
			Version:     version,
			AutoGate:    gateJSON(gate),
		})
	}
	if existing.State == EmbeddedStateRemoved {
		return nil // a rejected re-upload of a removed package changes nothing
	}
	return l.repo.Transition(ctx, packageID, []string{existing.State}, EmbeddedStateRejected,
		map[string]any{"auto_gate": gateJSON(gate)},
		"auto_gate_rejected", &owner, gate.Reason)
}

// bumpEmbeddedVersion: "3" → "4"; non-numeric tails (e.g. "default") become
// monotonic integers from the numeric prefix, else a timestamp-style suffix is
// out of scope — v1 keeps versions as plain integers, "default" → "1".
func bumpEmbeddedVersion(current string) string {
	n := 0
	for _, c := range current {
		if c < '0' || c > '9' {
			n = 0
			break
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return "1"
	}
	return fmt.Sprintf("%d", n+1)
}

// ── 管理端动作 ──────────────────────────────────────────────────────────────

func (l *EmbeddedPluginLifecycle) InitialApprove(ctx context.Context, packageID string, actor uuid.UUID, reason string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStatePendingInitialReview}, EmbeddedStateApprovedForBuild,
		map[string]any{"reviewer_user_id": actor, "review_reason": reason},
		"initial_approved", &actor, reason)
}

// RequestChanges: reason is REQUIRED (rule 2) — enforced here, not just in the
// handler, so no code path can produce an empty-opinion rejection.
func (l *EmbeddedPluginLifecycle) RequestChanges(ctx context.Context, packageID string, actor uuid.UUID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return &EmbeddedPluginValidationError{Code: "embedded_reason_required",
			Message: "「需修改」必须填写意见，指向具体的文件或问题——专家要照着它改。"}
	}
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStatePendingInitialReview}, EmbeddedStateRejected,
		map[string]any{"reviewer_user_id": actor, "review_reason": reason},
		"changes_requested", &actor, reason)
}

func (l *EmbeddedPluginLifecycle) RequestBuild(ctx context.Context, packageID string, actor uuid.UUID) error {
	return l.repo.Transition(ctx, packageID,
		[]string{EmbeddedStateApprovedForBuild, EmbeddedStateBuildFailed}, EmbeddedStateBuildRequested,
		map[string]any{"build_requested_by": actor, "build_requested_at": "NOW()"},
		"build_requested", &actor, "")
}

// MarkBuildFailed / MarkTesting / MarkRemoved: called by the deployment-host
// build tool (E-4), not by the portals.

func (l *EmbeddedPluginLifecycle) MarkBuildFailed(ctx context.Context, packageID, reason string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateBuildRequested}, EmbeddedStateBuildFailed,
		map[string]any{}, "build_failed", nil, reason)
}

func (l *EmbeddedPluginLifecycle) MarkTesting(ctx context.Context, packageID string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateBuildRequested}, EmbeddedStateTesting,
		map[string]any{}, "baked", nil, "镜像构建成功并部署")
}

func (l *EmbeddedPluginLifecycle) MarkRemoved(ctx context.Context, packageID string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateRemoveRequested}, EmbeddedStateRemoved,
		map[string]any{}, "removed", nil, "下次构建已从镜像剔除")
}

// FinalApprove: the manual trigger that opens the package to everyone (D-9:
// never automatic — the state machine has no edge into `approved` from
// build success).
func (l *EmbeddedPluginLifecycle) FinalApprove(ctx context.Context, packageID string, actor uuid.UUID, reason string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateTesting}, EmbeddedStateApproved,
		map[string]any{"final_approved_by": actor, "final_approved_at": "NOW()"},
		"final_approved", &actor, reason)
}

func (l *EmbeddedPluginLifecycle) Disable(ctx context.Context, packageID string, actor uuid.UUID, reason string) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateApproved}, EmbeddedStateDisabled,
		map[string]any{}, "disabled", &actor, reason)
}

func (l *EmbeddedPluginLifecycle) Enable(ctx context.Context, packageID string, actor uuid.UUID) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateDisabled}, EmbeddedStateApproved,
		map[string]any{}, "enabled", &actor, "")
}

func (l *EmbeddedPluginLifecycle) MarkRemove(ctx context.Context, packageID string, actor uuid.UUID, reason string) error {
	return l.repo.Transition(ctx, packageID,
		[]string{EmbeddedStateApproved, EmbeddedStateDisabled, EmbeddedStateRemoveRequested}, EmbeddedStateRemoveRequested,
		map[string]any{}, "remove_marked", &actor, reason)
}

func (l *EmbeddedPluginLifecycle) UnmarkRemove(ctx context.Context, packageID string, actor uuid.UUID) error {
	return l.repo.Transition(ctx, packageID, []string{EmbeddedStateRemoveRequested}, EmbeddedStateApproved,
		map[string]any{}, "remove_unmarked", &actor, "")
}

// ── 提交闸（E-5）────────────────────────────────────────────────────────────

// EmbeddedSelectionDecision is what the two submission doors ask: given a
// plugin id the run selected, may THIS user run it RIGHT NOW?
type EmbeddedSelectionDecision struct {
	Allowed bool
	Code    string // refusal code for writeEnginePreflightRejection
	Message string
}

// ResolveEmbeddedSelection answers the gate for ids that belong to the
// embedded track. It returns (nil, false) when the id is NOT an embedded id —
// the caller continues with the normal builtin path (unknown ids keep their
// existing behavior).
func (l *EmbeddedPluginLifecycle) ResolveEmbeddedSelection(ctx context.Context, userID uuid.UUID, pluginID string) (*EmbeddedSelectionDecision, bool, error) {
	if l == nil || l.repo == nil {
		return nil, false, nil
	}
	row, err := l.repo.GetByPackageID(ctx, pluginID)
	if err != nil {
		return nil, false, err
	}
	if row == nil {
		return nil, false, nil
	}
	decide := func(allowed bool, code, msg string) (*EmbeddedSelectionDecision, bool, error) {
		return &EmbeddedSelectionDecision{Allowed: allowed, Code: code, Message: msg}, true, nil
	}
	switch row.State {
	case EmbeddedStateApproved:
		return decide(true, "", "")
	case EmbeddedStateTesting:
		isTester, err := l.repo.IsTester(ctx, userID)
		if err != nil {
			return nil, false, err
		}
		if isTester {
			return decide(true, "", "")
		}
		return decide(false, "plugin_testing_not_open",
			"该内嵌插件处于「测试中」，仅测试白名单账号可选；管理员最终通过后全员可见。")
	case EmbeddedStateDisabled:
		return decide(false, "plugin_disabled", "该内嵌插件已被管理员停用。")
	default:
		return decide(false, "plugin_not_yet_approved",
			fmt.Sprintf("该内嵌插件当前状态为「%s」，尚未通过最终放行，不可用于评测。", EmbeddedStateLabel(row.State)))
	}
}

// EmbeddedSelectionRefusalError is the pre-flight refusal when a run selects
// an embedded-plugin id that this caller may not run right now (not yet
// finally approved / testing-but-not-whitelisted / disabled). It travels
// through writeEnginePreflightRejection like every other 409 — one dispatcher,
// both doors (the P2-2 rule).
type EmbeddedSelectionRefusalError struct {
	Code    string
	Message string
}

func (e *EmbeddedSelectionRefusalError) Error() string { return e.Message }

// GuardEmbeddedPluginSelection: the shared gate both doors call once the run's
// plugin ids are resolved. refusal is nil when every id is allowed (or is not
// an embedded id at all). ids already validated as builtin/measured-legal pass
// through untouched — this gate only ever ADDS refusals, never removes them.
func (l *EmbeddedPluginLifecycle) GuardEmbeddedPluginSelection(ctx context.Context, userID uuid.UUID, pluginIDs []string) error {
	for _, id := range pluginIDs {
		decision, isEmbedded, err := l.ResolveEmbeddedSelection(ctx, userID, id)
		if err != nil {
			return err
		}
		if !isEmbedded || decision.Allowed {
			continue
		}
		return &EmbeddedSelectionRefusalError{Code: decision.Code, Message: decision.Message}
	}
	return nil
}
