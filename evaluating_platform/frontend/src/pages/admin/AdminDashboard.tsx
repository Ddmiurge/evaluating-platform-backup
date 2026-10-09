import { useEffect, useState } from 'react'
import { Card, Col, Row, Statistic, Tag, Typography } from 'antd'
import { DatabaseOutlined, SafetyOutlined, TeamOutlined } from '@ant-design/icons'
import { adminService, type AdminOverview } from '../../services/admin'

const { Text, Title } = Typography

export function AdminDashboard() {
  const [overview, setOverview] = useState<AdminOverview | null>(null)

  useEffect(() => {
    adminService.getOverview().then(setOverview).catch(() => {})
  }, [])

  const cards = [
    { title: '平台用户', value: overview?.total_users ?? 0, icon: <TeamOutlined />, color: '#2563eb' },
    { title: '发布资源', value: overview?.published_resources ?? 0, icon: <SafetyOutlined />, color: '#16a34a' },
    { title: 'shadow 资源', value: overview?.shadow_resources ?? 0, icon: <DatabaseOutlined />, color: '#7c3aed' },
  ]

  return (
    <div>
      <div style={{ marginBottom: 20 }}>
        <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>平台概览</Title>
        <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
          当前评测执行与编排由 maclaw-runtime 承担，管理员门户负责运营治理与模型配置。
        </Text>
      </div>
      <Row gutter={16} style={{ marginBottom: 20 }}>
        {cards.map((card) => (
          <Col span={8} key={card.title}>
            <Card style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: 8 }}>
              <Statistic
                title={card.title}
                value={card.value}
                styles={{ content: { color: card.color, fontWeight: 700 } }}
                prefix={card.icon}
              />
            </Card>
          </Col>
        ))}
      </Row>
      <Card title="角色分布" style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: 8 }}>
        {Object.entries(overview?.users_by_role || {}).map(([role, count]) => (
          <div key={role} style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
            <Tag color={role === 'admin' ? 'purple' : role === 'expert' ? 'orange' : 'blue'}>{role}</Tag>
            <Text style={{ color: 'var(--text-primary)', fontWeight: 600 }}>{count}</Text>
          </div>
        ))}
      </Card>
    </div>
  )
}