import { Card, Typography, Descriptions, Tag } from 'antd'
import { useAuth } from '../../context/AuthContext'

const { Title, Text } = Typography

const roleLabel: Record<string, string> = {
  enterprise: '企业客户',
  expert: '安全专家',
  admin: '平台管理员',
}

export function Settings() {
  const { user } = useAuth()

  return (
    <div style={{ maxWidth: 600, display: 'flex', flexDirection: 'column', gap: 24 }}>
      <div style={{ marginBottom: 24 }}>
        <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>账户设置</Title>
        <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>查看账户信息</Text>
      </div>

      <Card style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
        <Descriptions
          column={1}
          labelStyle={{ color: 'var(--text-muted)', width: 120 }}
          contentStyle={{ color: 'var(--text-primary)' }}
        >
          <Descriptions.Item label="姓名">{user?.name || '—'}</Descriptions.Item>
          <Descriptions.Item label="邮箱">{user?.email || '—'}</Descriptions.Item>
          <Descriptions.Item label="机构">{user?.org_name || '—'}</Descriptions.Item>
          <Descriptions.Item label="角色">
            <Tag color="blue">{roleLabel[user?.role || ''] || user?.role}</Tag>
          </Descriptions.Item>
          <Descriptions.Item label="账号状态">
            <Tag color={user?.is_active ? 'success' : 'error'}>
              {user?.is_active ? '正常' : '已禁用'}
            </Tag>
          </Descriptions.Item>
        </Descriptions>

        <div style={{
          marginTop: 20, padding: '12px 16px',
          background: 'var(--color-primary-light)',
          border: '1px solid var(--color-primary-border)',
          borderRadius: 6,
        }}>
          <Text style={{ color: 'var(--text-muted)', fontSize: 13 }}>
            需要修改密码或账户信息？请联系平台管理员：admin@platform.local
          </Text>
        </div>
      </Card>
    </div>
  )
}