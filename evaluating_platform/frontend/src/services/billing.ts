import api from './api'

export interface BillingRecord {
  id: string
  user_id: string
  assessment_id?: string
  tool_name: string
  tool_category: string
  call_count: number
  tokens_used: number
  amount: number
  asset_id?: string
  expert_id?: string
  expert_share: number
  created_at: string
}

export interface BalanceTransaction {
  id: string
  user_id: string
  type: 'recharge' | 'deduct' | 'refund'
  amount: number
  balance_before: number
  balance_after: number
  description: string
  created_at: string
}

export const billingService = {
  async getBalance(): Promise<{ balance: number }> {
    const res = await api.get('/billing/balance')
    return res.data
  },

  async getRecords(limit = 20, offset = 0): Promise<{ items: BillingRecord[]; total: number }> {
    const res = await api.get('/billing/records', { params: { limit, offset } })
    return res.data
  },

  async getTransactions(limit = 20, offset = 0): Promise<{ items: BalanceTransaction[]; total: number }> {
    const res = await api.get('/billing/transactions', { params: { limit, offset } })
    return res.data
  },

  async recharge(amount: number): Promise<{ new_balance: number }> {
    const res = await api.post('/billing/recharge', { amount })
    return res.data
  },

  async getEarnings(limit = 20, offset = 0): Promise<{ items: BillingRecord[]; total: number; total_earnings: number }> {
    const res = await api.get('/billing/earnings', { params: { limit, offset } })
    return res.data
  },
}
