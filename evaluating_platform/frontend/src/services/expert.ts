import api from './api'

export interface MaclawMCPToolSummary {
  name: string
  description?: string
  input_schema?: Record<string, unknown>
}

export interface MaclawMCPServerSummary {
  id: string
  kind: string
  name: string
  endpoint_url?: string
  auth_type?: string
  has_auth_secret?: boolean
  header_names?: string[]
  disabled?: boolean
  auto_start?: boolean
  source?: string
  running?: boolean
  health_status?: string
  fail_count?: number
  last_check_at?: string
  created_at?: string
  tools?: MaclawMCPToolSummary[]
}

export interface MaclawMCPServerInput {
  kind?: 'remote'
  name: string
  endpoint_url: string
  auth_type?: string
  auth_secret?: string
  headers?: Record<string, string>
  disabled?: boolean
  auto_start?: boolean
}

export const expertService = {
  uploadSample: (data: FormData) =>
    api.post('/samples', data, {
      headers: { 'Content-Type': 'multipart/form-data' },
    }).then((r) => r.data),

  listSamples: (params?: { sub_type?: string; limit?: number; offset?: number }) =>
    api.get('/samples', { params }).then((r) => r.data),

  deleteSample: (id: string) => api.delete(`/samples/${id}`).then((r) => r.data),

  previewSample: (id: string, limit?: number) =>
    api.get(`/samples/${id}/preview`, { params: { limit } }).then((r) => r.data),

  uploadComposedAttack: (data: FormData) =>
    api.post('/composed-attacks', data, {
      headers: { 'Content-Type': 'multipart/form-data' },
    }).then((r) => r.data),

  listComposedAttacks: (params?: { sub_type?: string; limit?: number; offset?: number }) =>
    api.get('/composed-attacks', { params }).then((r) => r.data),

  deleteComposedAttack: (id: string) => api.delete(`/composed-attacks/${id}`).then((r) => r.data),

  previewComposedAttack: (id: string, limit?: number) =>
    api.get(`/composed-attacks/${id}/preview`, { params: { limit } }).then((r) => r.data),

  createTemplate: (data: {
    sub_type: string
    name: string
    description?: string
    content: string
    variables?: { name: string; description: string; required: boolean; default?: string }[]
  }) => api.post('/templates', data).then((r) => r.data),

  listTemplates: (params?: { sub_type?: string; limit?: number; offset?: number }) =>
    api.get('/templates', { params }).then((r) => r.data),

  updateTemplate: (id: string, data: {
    sub_type: string
    name: string
    description?: string
    content: string
    variables?: { name: string; description: string; required: boolean; default?: string }[]
  }) => api.put(`/templates/${id}`, data).then((r) => r.data),

  deleteTemplate: (id: string) => api.delete(`/templates/${id}`).then((r) => r.data),

  uploadTemplateCSV: (data: FormData) =>
    api.post('/templates/upload-csv', data, {
      headers: { 'Content-Type': 'multipart/form-data' },
    }).then((r) => r.data),

  listMaclawSkills: (limit = 100) =>
    api.get('/maclaw/skills', { params: { limit } }).then((r) => r.data),

  searchMaclawSkills: (data: {
    query: string
    sources?: string[]
    top_n?: number
    skill_hub_url?: string
    skill_market_url?: string
    github_token?: string
    include_installed?: boolean
  }) => api.post('/maclaw/skills/search', data).then((r) => r.data),

  importMaclawSkill: (data: {
    zip_base64: string
    overwrite?: boolean
    archive_name?: string
  }) => api.post('/maclaw/skills/import', data).then((r) => r.data),

  installMaclawSkill: (data: {
    source: string
    repo_url?: string
    raw_url?: string
    repo_full_name?: string
    file_path?: string
    branch?: string
    definition_type?: string
    zip_base64?: string
    skill_hub_url?: string
    skill_id?: string
    overwrite?: boolean
    github_token?: string
  }) => api.post('/maclaw/skills/install', data).then((r) => r.data),

  listMaclawMCPServers: (limit = 100) =>
    api.get<{ items: MaclawMCPServerSummary[] }>('/maclaw/mcp/servers', { params: { limit } }).then((r) => r.data),

  createMaclawMCPServer: (data: MaclawMCPServerInput) =>
    api.post<MaclawMCPServerSummary>('/maclaw/mcp/servers', data).then((r) => r.data),

  updateMaclawMCPServer: (id: string, data: MaclawMCPServerInput) =>
    api.patch<MaclawMCPServerSummary>(`/maclaw/mcp/servers/${encodeURIComponent(id)}`, data).then((r) => r.data),

  deleteMaclawMCPServer: (id: string) =>
    api.delete(`/maclaw/mcp/servers/${encodeURIComponent(id)}`).then((r) => r.data),

  startMaclawMCPServer: (id: string) =>
    api.post<MaclawMCPServerSummary>(`/maclaw/mcp/servers/${encodeURIComponent(id)}/start`).then((r) => r.data),

  stopMaclawMCPServer: (id: string) =>
    api.post<MaclawMCPServerSummary>(`/maclaw/mcp/servers/${encodeURIComponent(id)}/stop`).then((r) => r.data),

  healthCheckMaclawMCPServer: (id: string) =>
    api.post<MaclawMCPServerSummary>(`/maclaw/mcp/servers/${encodeURIComponent(id)}/health-check`).then((r) => r.data),

  listMaclawMCPServerTools: (id: string) =>
    api.get<{ items: MaclawMCPToolSummary[] }>(`/maclaw/mcp/servers/${encodeURIComponent(id)}/tools`).then((r) => r.data),

  // ── U10 · 专家自定义插件（首期仅 yaml 形态）───────────────────────────────
  // 发布走既有的加密资源存储（POST /maclaw/evaluation/resources，kind=pf_plugin），
  // 不另建一套上传接口：发布 → 分发 → 管理员治理全部复用 maclaw_resources +
  // maclaw_resource_publications，与设计文档 §2 U10 ① 一致。
  publishPromptfooPlugin: (data: PromptfooPluginPublishInput) =>
    api.post<PromptfooPluginSummary>('/maclaw/evaluation/resources', data).then((r) => r.data),

  listPromptfooPlugins: (params?: { include_inactive?: boolean; limit?: number }) =>
    api
      .get<{ items: PromptfooPluginSummary[] }>('/maclaw/evaluation/resources', {
        params: { kind: 'pf_plugin', ...params },
      })
      .then((r) => r.data),
}

/** U10 · 一条已发布的自定义插件（只含安全摘要，不含插件正文）。 */
export interface PromptfooPluginSummary {
  id: string
  handle: string
  name: string
  description?: string
  kind: string
  version?: string
  status: string
  enabled: boolean
  summary?: string
  tags?: string[]
  metadata?: Record<string, string>
  created_at?: string
  updated_at?: string
}

/**
 * U10 · 发布请求。
 *
 * `pf_plugin_form` 固定为 'yaml'：首期仅支持 YAML 形态。后端仍会独立校验
 * （含对伪装成 YAML 的 JS/脚本内容的嗅探），前端声明只是让「形态填错」得到
 * 一条清楚的提示，而不是一条 schema 报错。
 */
export interface PromptfooPluginPublishInput {
  name: string
  description?: string
  kind: 'pf_plugin'
  version?: string
  status: 'published'
  enabled: boolean
  summary?: string
  tags?: string[]
  /** 插件包正文（YAML：generator / grader / 可选 threshold / metric / id）。 */
  payload: string
  metadata: {
    pf_plugin_form: 'yaml'
    /** 逗号分隔的依赖插件 id；命中远程生成清单时后端会拒绝发布。 */
    pf_plugin_depends_on?: string
    [key: string]: string | undefined
  }
}