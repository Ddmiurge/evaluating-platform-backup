import { Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom'
import { Layout, Menu, Typography, Button, Badge, Avatar, Dropdown } from 'antd'
import { useEffect, useState } from 'react'
import {
  SettingOutlined,
  LogoutOutlined,
  UserOutlined,
  BellOutlined,
  WalletOutlined,
  RobotOutlined, SafetyOutlined } from '@ant-design/icons'
import { BillingPage } from './BillingPage'
import { Settings } from './Settings'
import { ChatPage } from './ChatPage'
import { EngineEvalPage } from './EngineEvalPage'
import { useAuth } from '../../context/AuthContext'
import { billingService } from '../../services/billing'
import appLogoUrl from '../../assets/qianxin-ai-security-logo.png'

const { Sider, Header, Content } = Layout
const { Text } = Typography

const menuItems = [
  { key: '/enterprise', icon: <RobotOutlined />, label: <span>AI 服务 <Badge count="荐" size="small" offset={[6, -2]} /></span> },
  { key: '/enterprise/eval', icon: <SafetyOutlined />, label: '自选评测' },
  { key: '/enterprise/billing', icon: <WalletOutlined />, label: '计费管理' },
  { key: '/enterprise/settings', icon: <SettingOutlined />, label: '账户设置' },
]

export function EnterprisePortal() {
  const location = useLocation()
  const navigate = useNavigate()
  const { user, logout } = useAuth()
  const [balance, setBalance] = useState<number | null>(null)

  useEffect(() => {
    billingService.getBalance().then(r => setBalance(r.balance)).catch(() => {})
  }, [])

  useEffect(() => {
    if (/^\/enterprise\/skills\/[^/]+\/open$/.test(location.pathname)) {
      navigate('/enterprise', { replace: true })
    }
  }, [location.pathname, navigate])

  const selectedKey = location.pathname === '/enterprise' ? '/enterprise' : location.pathname
  const paddedContent = location.pathname === '/enterprise/eval' || location.pathname === '/enterprise/billing' || location.pathname === '/enterprise/settings'

  const userMenu = [
    {
      key: 'logout',
      icon: <LogoutOutlined />,
      label: '退出登录',
      onClick: () => {
        logout()
        navigate('/login')
      },
    },
  ]

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
        <div style={{
          height: 64,
          display: 'flex',
          alignItems: 'center',
          paddingLeft: 20,
          borderBottom: '1px solid var(--border-color)',
          gap: 10,
        }}>
          <div style={{
            width: 42,
            height: 42,
            borderRadius: '50%',
            background: 'transparent',
            overflow: 'hidden',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
          }}>
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
            <div style={{ color: 'var(--text-muted)', fontSize: 11 }}>企业客户门户</div>
          </div>
        </div>

        <Menu
          mode="inline"
          selectedKeys={[selectedKey]}
          style={{ background: 'transparent', border: 'none', marginTop: 8 }}
          items={menuItems.map(item => ({
            key: item.key,
            icon: item.icon,
            label: <Link to={item.key}>{item.label}</Link>,
          }))}
        />

        <div style={{
          position: 'absolute',
          bottom: 20,
          left: 12,
          right: 12,
          padding: '12px 16px',
          background: 'var(--bg-card)',
          border: '1px solid var(--border-color)',
          borderRadius: 8,
        }}>
          <Text style={{ color: 'var(--text-muted)', fontSize: 11 }}>账户余额</Text>
          <div style={{ color: 'var(--color-primary)', fontSize: 18, fontWeight: 700, fontVariantNumeric: 'tabular-nums' }}>
            {balance !== null ? `¥ ${balance.toFixed(2)}` : '—'}
          </div>
          <Button
            type="primary"
            size="small"
            block
            style={{ marginTop: 8, fontSize: 12 }}
            onClick={() => navigate('/enterprise/billing')}
          >
            充值
          </Button>
        </div>
      </Sider>

      <Layout style={{ marginLeft: 220 }}>
        <Header style={{
          background: 'var(--bg-surface)',
          borderBottom: '1px solid var(--border-color)',
          padding: '0 24px',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          position: 'sticky',
          top: 0,
          zIndex: 99,
        }}>
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>
            {new Date().toLocaleDateString('zh-CN', { year: 'numeric', month: 'long', day: 'numeric' })}
          </Text>

          <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
            <Badge count={0} size="small">
              <BellOutlined style={{ color: 'var(--text-secondary)', fontSize: 18, cursor: 'pointer' }} />
            </Badge>
            <Dropdown menu={{ items: userMenu }} placement="bottomRight">
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                <Avatar
                  size={32}
                  icon={<UserOutlined />}
                  style={{ background: 'var(--color-primary-light)', border: '1px solid var(--color-primary-border)' }}
                />
                <Text style={{ color: 'var(--text-primary)', fontSize: 13 }}>
                  {user?.name || user?.email || '用户'}
                </Text>
              </div>
            </Dropdown>
          </div>
        </Header>

        <Content style={{ padding: paddedContent ? 24 : 0, minHeight: 'calc(100vh - 64px)' }}>
          <Routes>
            <Route path="eval" element={<EngineEvalPage />} />
            <Route path="billing" element={<BillingPage />} />
            <Route path="settings" element={<Settings />} />
            <Route path="*" element={<ChatPage />} />
          </Routes>
        </Content>
      </Layout>
    </Layout>
  )
}