import api from './api'
import type { User } from './auth'

export interface AdminOverview {
  total_users: number
  users_by_role: Record<string, number>
  maclaw_mapped_accounts: number
  published_resources: number
  total_resource_publications: number
  shadow_resources: number
  maclaw_runtime_configured: boolean
}

export interface RuntimeLLMProvider {
  name?: string
  url?: string
  key?: string
  model?: string
  wire_api?: string
  protocol?: string
  context_length?: number
  timeout_sec?: number
  supports_vision?: boolean
  agent_type?: string
  auth_type?: string
}

export interface RuntimeAppConfig {
  maclaw_llm_url?: string
  maclaw_llm_key?: string
  maclaw_llm_model?: string
  maclaw_llm_protocol?: string
  maclaw_llm_context_length?: number
  maclaw_llm_timeout_sec?: number
  maclaw_llm_current_provider?: string
  maclaw_llm_providers?: RuntimeLLMProvider[]
  remote_hub_url?: string
  skill_sources_allowed?: string[]
}

export interface RuntimeUserConfig {
  tenant_id?: string
  user_id?: string
  app_config: RuntimeAppConfig
  updated_at?: string
}

export interface RuntimeConfigValidation {
  valid: boolean
  issues?: { key?: string; message?: string }[]
}

export interface RuntimeConfigTestResult {
  success: boolean
  message?: string
  error?: string
  detail?: string
  latency_ms?: number
  endpoint?: string
  provider_name?: string
  model?: string
  protocol?: string
  wire_api?: string
  validation?: RuntimeConfigValidation
}

export interface AdminMaclawAccount {
  platform_user_id: string
  email: string
  name: string
  role: string
  org_name?: string
  is_active: boolean
  maclaw_tenant_id?: string
  maclaw_user_id?: string
  maclaw_instance_id?: string
  provisioning_status?: string
  model_config_status?: string
  last_error?: string
}

export interface AdminMaclawResource {
  source_expert_user_id: string
  source_expert_name?: string
  source_expert_email?: string
  source_maclaw_tenant_id: string
  source_resource_id: string
  source_resource_handle: string
  source_version: string
  name: string
  kind: string
  status: string
  enabled: boolean
  summary?: string
  assessment_types?: string[]
  tags?: string[]
  metadata?: Record<string, string>
  created_at?: string
  updated_at?: string
}

export interface AdminMaclawJob {
  platform_user_id: string
  platform_role: string
  platform_email: string
  maclaw_tenant_id: string
  maclaw_instance_id: string
  job: {
    id: string
    kind: string
    status: string
    created_at?: string
    updated_at?: string
  }
}

export interface MaclawHubConfig {
  hub_url?: string
  enabled: boolean
  allowed_sources: string[]
  updated_at?: string
}

export interface MaclawHubSyncResult {
  attempted: number
  succeeded: number
  failed: number
}

export interface MaclawHubStatusItem {
  platform_user_id: string
  platform_role: string
  platform_email: string
  maclaw_tenant_id: string
  maclaw_user_id: string
  maclaw_instance_id: string
  provisioning_status?: string
}

export const adminService = {
  async listUsers(params?: {
    limit?: number
    offset?: number
    role?: string
    keyword?: string
  }): Promise<{ items: User[]; total: number }> {
    const res = await api.get('/admin/users', { params })
    return res.data
  },

  async updateUserRole(id: string, role: string): Promise<void> {
    await api.put(`/admin/users/${id}/role`, { role })
  },

  async setUserActive(id: string, active: boolean): Promise<void> {
    await api.put(`/admin/users/${id}/active`, { active })
  },

  async deleteUser(id: string): Promise<void> {
    await api.delete(`/admin/users/${id}`)
  },

  async getOverview(): Promise<AdminOverview> {
    const res = await api.get<AdminOverview>('/admin/overview')
    return res.data
  },

  async listMaclawAccounts(params?: { role?: string; keyword?: string; limit?: number; offset?: number }) {
    const res = await api.get<{ items: AdminMaclawAccount[]; total: number }>('/admin/maclaw/accounts', { params })
    return res.data
  },

  async getDefaultModelConfig() {
    const res = await api.get<RuntimeUserConfig>('/admin/maclaw/model-default')
    return res.data
  },

  async updateDefaultModelConfig(data: RuntimeAppConfig) {
    const res = await api.put<RuntimeUserConfig>('/admin/maclaw/model-default', data)
    return res.data
  },

  async validateDefaultModelConfig(data?: RuntimeAppConfig) {
    const res = await api.post<RuntimeConfigValidation>('/admin/maclaw/model-default/validate', data || {})
    return res.data
  },

  async testDefaultModelConfig(data?: RuntimeAppConfig) {
    const res = await api.post<RuntimeConfigTestResult>('/admin/maclaw/model-default/test', data || {})
    return res.data
  },

  async getAccountModelConfig(userId: string) {
    const res = await api.get<RuntimeUserConfig>(`/admin/maclaw/accounts/${userId}/config`)
    return res.data
  },

  async updateAccountModelConfig(userId: string, data: RuntimeAppConfig) {
    const res = await api.put<RuntimeUserConfig>(`/admin/maclaw/accounts/${userId}/config`, data)
    return res.data
  },

  async validateAccountModelConfig(userId: string, data?: RuntimeAppConfig) {
    const res = await api.post<RuntimeConfigValidation>(`/admin/maclaw/accounts/${userId}/config/validate`, data || {})
    return res.data
  },

  async testAccountModelConfig(userId: string, data?: RuntimeAppConfig) {
    const res = await api.post<RuntimeConfigTestResult>(`/admin/maclaw/accounts/${userId}/config/test`, data || {})
    return res.data
  },

  async getMaclawHubConfig() {
    const res = await api.get<MaclawHubConfig>('/admin/maclaw/hub-config')
    return res.data
  },

  async updateMaclawHubConfig(data: MaclawHubConfig) {
    const res = await api.put<{ config: MaclawHubConfig; sync: MaclawHubSyncResult }>('/admin/maclaw/hub-config', data)
    return res.data
  },

  async syncMaclawHubConfig() {
    const res = await api.post<MaclawHubSyncResult>('/admin/maclaw/hub-config/sync')
    return res.data
  },

  async getMaclawHubConfigStatus() {
    const res = await api.get<{ items: MaclawHubStatusItem[]; total: number }>('/admin/maclaw/hub-config/status')
    return res.data
  },

  async listMaclawResources(params?: { kind?: string; query?: string; include_inactive?: boolean; limit?: number }) {
    const res = await api.get<{ items: AdminMaclawResource[]; total: number }>('/admin/maclaw/resources', { params })
    return res.data
  },

  async updateMaclawResource(id: string, data: { enabled?: boolean; status?: string }) {
    const res = await api.patch<AdminMaclawResource>(`/admin/maclaw/resources/${encodeURIComponent(id)}`, data)
    return res.data
  },

  async listMaclawJobs(params?: { status?: string; kind?: string; limit?: number; offset?: number }) {
    const res = await api.get<{ items: AdminMaclawJob[]; total: number }>('/admin/maclaw/jobs', { params })
    return res.data
  },
}
