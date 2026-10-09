import { Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom'
import { Layout, Menu, Typography, Avatar, Dropdown, Badge } from 'antd'
import { useEffect, useState } from 'react'
import {
  CodeOutlined,
  LogoutOutlined,
  UserOutlined,
  BellOutlined,
  ExperimentOutlined,
  RocketOutlined,
  ApiOutlined,
} from '@ant-design/icons'
import { SampleManager } from './SampleManager'
import { EngineManager } from './EngineManager'
import { MaclawSkillManager } from './MaclawSkillManager'
import { MaclawMCPServerManager } from './MaclawMCPServerManager'
import { useAuth } from '../../context/AuthContext'
import { billingService } from '../../services/billing'
import appLogoUrl from '../../assets/qianxin-ai-security-logo.png'

const { Sider, Header, Content } = Layout
const { Text } = Typography

const menuItems = [
  { key: '/expert/samples', icon: <ExperimentOutlined />, label: '样本管理' },
  { key: '/expert/engine', icon: <RocketOutlined />, label: '引擎管理' },
  { key: '/expert/skills', icon: <CodeOutlined />, label: 'Skill 管理' },
  { key: '/expert/mcp', icon: <ApiOutlined />, label: 'MCP 服务' },
]

export function ExpertPortal() {
  const location = useLocation()
  const navigate = useNavigate()
  const { user, logout } = useAuth()
  const [earnings, setEarnings] = useState<number | null>(null)

  useEffect(() => {
    billingService.getEarnings(5, 0).then((r) => setEarnings(r.total_earnings || 0)).catch(() => {})
  }, [])

  return (
    <Layout style={{ minHeight: '100vh', background: 'var(--bg-base)' }}>
      <Sider
        width={220}
        style={{
          background: 'var(--bg-surface)',
          borderRight: '1px solid var(--border-color)',
          position: 'fixed',
          height: '100vh',
          left: 0,
          top: 0,
          zIndex: 100,
        }}
      >
        <div
          style={{
            height: 64,
            display: 'flex',
            alignItems: 'center',
            paddingLeft: 20,
            borderBottom: '1px solid var(--border-color)',
            gap: 10,
          }}
        >
          <div
            style={{
              width: 42,
              height: 42,
              borderRadius: '50%',
              background: 'transparent',
              overflow: 'hidden',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <img
              src={appLogoUrl}
              alt="Qianxin China-ASEAN AI Security Research Institute"
              style={{ width: '100%', height: '100%', objectFit: 'contain' }}
            />
          </div>
          <div>
            <div style={{ color: 'var(--text-primary)', fontWeight: 700, fontSize: 14, lineHeight: 1.2 }}>
              AI 安全评估
            </div>
            <div style={{ color: 'var(--text-muted)', fontSize: 11 }}>安全专家门户</div>
          </div>
        </div>

        <Menu
          mode="inline"
          selectedKeys={[location.pathname]}
          style={{ background: 'transparent', border: 'none', marginTop: 8 }}
          items={menuItems.map((item) => ({
            key: item.key,
            icon: item.icon,
            label: <Link to={item.key}>{item.label}</Link>,
          }))}
        />

        <div
          style={{
            position: 'absolute',
            bottom: 20,
            left: 12,
            right: 12,
            padding: '12px 16px',
            background: 'var(--bg-card)',
            border: '1px solid var(--border-color)',
            borderRadius: 8,
          }}
        >
          <Text style={{ color: 'var(--text-muted)', fontSize: 11 }}>专家累计收益</Text>
          <div style={{ color: 'var(--color-high)', fontSize: 18, fontWeight: 700 }}>
            {earnings !== null ? `¥ ${earnings.toFixed(2)}` : '—'}
          </div>
          <Text style={{ color: 'var(--text-muted)', fontSize: 11 }}>工具调用分成</Text>
        </div>
      </Sider>

      <Layout style={{ marginLeft: 220 }}>
        <Header
          style={{
            background: 'var(--bg-surface)',
            borderBottom: '1px solid var(--border-color)',
            padding: '0 24px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            position: 'sticky',
            top: 0,
            zIndex: 99,
          }}
        >
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>安全专家工作台</Text>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
            <Badge count={0} size="small">
              <BellOutlined style={{ color: 'var(--text-secondary)', fontSize: 18, cursor: 'pointer' }} />
            </Badge>
            <Dropdown
              menu={{
                items: [
                  {
                    key: 'logout',
                    icon: <LogoutOutlined />,
                    label: '退出',
                    onClick: () => {
                      logout()
                      navigate('/login')
                    },
                  },
                ],
              }}
              placement="bottomRight"
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                <Avatar
                  size={32}
                  icon={<UserOutlined />}
                  style={{ background: 'rgba(234, 88, 12, 0.12)', border: '1px solid rgba(234, 88, 12, 0.4)' }}
                />
                <Text style={{ color: 'var(--text-primary)', fontSize: 13 }}>{user?.name || '专家用户'}</Text>
              </div>
            </Dropdown>
          </div>
        </Header>

        <Content style={{ padding: 24 }}>
          <Routes>
            <Route path="samples" element={<SampleManager />} />
            <Route path="engine" element={<EngineManager />} />
            <Route path="skills" element={<MaclawSkillManager />} />
            <Route path="mcp" element={<MaclawMCPServerManager />} />
            <Route path="*" element={<SampleManager />} />
          </Routes>
        </Content>
      </Layout>
    </Layout>
  )
}