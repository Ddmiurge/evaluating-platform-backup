import { useEffect, useState } from 'react'
import {
  Card, Tabs, Typography, Statistic, Button, Table, Tag, Modal, InputNumber, message, Space
} from 'antd'
import { WalletOutlined, PlusOutlined } from '@ant-design/icons'
import { billingService, type BillingRecord, type BalanceTransaction } from '../../services/billing'

const { Title, Text } = Typography

export function BillingPage() {
  const [balance, setBalance] = useState<number | null>(null)
  const [records, setRecords] = useState<BillingRecord[]>([])
  const [transactions, setTransactions] = useState<BalanceTransaction[]>([])
  const [recordTotal, setRecordTotal] = useState(0)
  const [txTotal, setTxTotal] = useState(0)
  const [rechargeOpen, setRechargeOpen] = useState(false)
  const [rechargeAmount, setRechargeAmount] = useState<number>(100)
  const [loading, setLoading] = useState(false)

  const loadBalance = () => {
    billingService.getBalance().then(r => setBalance(r.balance)).catch(() => {})
  }

  useEffect(() => {
    loadBalance()
    billingService.getRecords(20, 0).then(r => { setRecords(r.items || []); setRecordTotal(r.total) }).catch(() => {})
    billingService.getTransactions(20, 0).then(r => { setTransactions(r.items || []); setTxTotal(r.total) }).catch(() => {})
  }, [])

  const handleRecharge = async () => {
    setLoading(true)
    try {
      const res = await billingService.recharge(rechargeAmount)
      message.success(`充值成功，新余额：¥${res.new_balance?.toFixed(2) ?? ''}`)
      setRechargeOpen(false)
      loadBalance()
    } catch (err: unknown) {
      message.error((err as Error).message || '充值失败')
    } finally {
      setLoading(false)
    }
  }

  const recordColumns = [
    { title: '工具', dataIndex: 'tool_name', render: (v: string) => <Text style={{ color: 'var(--text-primary)' }}>{v}</Text> },
    { title: '类型', dataIndex: 'tool_category', render: (v: string) => <Tag>{v || '—'}</Tag> },
    { title: '用量', dataIndex: 'tokens_used', render: (v: number) => <Text style={{ color: 'var(--text-secondary)' }}>{v} tokens</Text> },
    { title: '金额', dataIndex: 'amount', render: (v: number) => <Text style={{ color: 'var(--color-high)', fontWeight: 600 }}>-¥{v?.toFixed(4)}</Text> },
    { title: '时间', dataIndex: 'created_at', render: (v: string) => <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>{v?.slice(0, 16).replace('T', ' ')}</Text> },
  ]

  const txColumns = [
    { title: '类型', dataIndex: 'type', render: (v: string) => (
      <Tag color={v === 'recharge' ? 'success' : 'error'}>{v === 'recharge' ? '充值' : '扣费'}</Tag>
    )},
    { title: '金额', dataIndex: 'amount', render: (v: number, r: BalanceTransaction) => (
      <Text style={{ color: r.type === 'recharge' ? 'var(--color-low)' : 'var(--color-high)', fontWeight: 600 }}>
        {r.type === 'recharge' ? '+' : '-'}¥{Math.abs(v)?.toFixed(4)}
      </Text>
    )},
    { title: '变动后余额', dataIndex: 'balance_after', render: (v: number) => <Text style={{ color: 'var(--text-secondary)' }}>¥{v?.toFixed(4)}</Text> },
    { title: '备注', dataIndex: 'description', render: (v: string) => <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>{v || '—'}</Text> },
    { title: '时间', dataIndex: 'created_at', render: (v: string) => <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>{v?.slice(0, 16).replace('T', ' ')}</Text> },
  ]

  const items = [
    {
      key: 'overview',
      label: '余额概览',
      children: (
        <Card style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 40 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
              <div style={{
                width: 56, height: 56, borderRadius: 14,
                background: 'var(--color-primary-light)',
                border: '1px solid var(--color-primary-border)',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
              }}>
                <WalletOutlined style={{ color: 'var(--color-primary)', fontSize: 24 }} />
              </div>
              <Statistic
                title={<Text style={{ color: 'var(--text-secondary)' }}>可用余额</Text>}
                value={balance ?? 0}
                prefix="¥"
                precision={2}
                styles={{ content: { color: 'var(--color-primary)', fontSize: 36, fontWeight: 700 } }}
              />
            </div>
            <Button type="primary" icon={<PlusOutlined />} size="large"
              onClick={() => setRechargeOpen(true)}>
              立即充值
            </Button>
          </div>
        </Card>
      ),
    },
    {
      key: 'records',
      label: `账单明细 (${recordTotal})`,
      children: (
        <Table
          dataSource={records}
          columns={recordColumns}
          rowKey="id"
          pagination={{ total: recordTotal, pageSize: 20 }}
          style={{ background: 'transparent' }}
        />
      ),
    },
    {
      key: 'transactions',
      label: `交易记录 (${txTotal})`,
      children: (
        <Table
          dataSource={transactions}
          columns={txColumns}
          rowKey="id"
          pagination={{ total: txTotal, pageSize: 20 }}
          style={{ background: 'transparent' }}
        />
      ),
    },
  ]

  return (
    <div>
      <div style={{ marginBottom: 24 }}>
        <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>计费管理</Title>
        <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>管理账户余额和查看消费记录</Text>
      </div>

      <Tabs items={items} />

      <Modal
        title="账户充值"
        open={rechargeOpen}
        onCancel={() => setRechargeOpen(false)}
        footer={
          <Space>
            <Button onClick={() => setRechargeOpen(false)}>取消</Button>
            <Button type="primary" loading={loading} onClick={handleRecharge}>确认充值</Button>
          </Space>
        }
      >
        <div style={{ padding: '20px 0' }}>
          <Text style={{ color: 'var(--text-secondary)' }}>充值金额（元）</Text>
          <div style={{ marginTop: 8 }}>
            <InputNumber
              min={1} max={100000}
              value={rechargeAmount}
              onChange={v => setRechargeAmount(v || 100)}
              prefix="¥"
              style={{ width: '100%' }}
              size="large"
            />
          </div>
          <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
            {[100, 500, 1000, 5000].map(v => (
              <Button key={v} size="small" onClick={() => setRechargeAmount(v)}>¥{v}</Button>
            ))}
          </div>
        </div>
      </Modal>
    </div>
  )
}