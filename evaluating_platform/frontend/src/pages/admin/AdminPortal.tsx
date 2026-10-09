import { Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom'
import { Layout, Menu, Typography, Avatar, Dropdown } from 'antd'
import {
  ApiOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  LogoutOutlined,
  SettingOutlined,
  TeamOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { AdminDashboard } from './AdminDashboard'
import { UserManagement } from './UserManagement'
import { AccountTenantManagement } from './AccountTenantManagement'
import { ResourceGovernance } from './ResourceGovernance'
import { JobGovernance } from './JobGovernance'
import { MaclawModelConfig } from './MaclawModelConfig'
import { MaclawHubManagement } from './MaclawHubManagement'
import { SystemHealth } from './SystemHealth'
import { useAuth } from '../../context/AuthContext'
import appLogoUrl from '../../assets/qianxin-ai-security-logo.png'

const { Sider, Header, Content } = Layout
const { Text } = Typography

export function AdminPortal() {
  const location = useLocation()
  const navigate = useNavigate()
  const { user, logout } = useAuth()

  const menuItems = [
    { key: '/admin/dashboard', icon: <DashboardOutlined />, label: '平台概览' },
    { key: '/admin/users', icon: <TeamOutlined />, label: '用户管理' },
    { key: '/admin/accounts', icon: <DatabaseOutlined />, label: '用户与租户' },
    { key: '/admin/resources', icon: <ApiOutlined />, label: '资源治理' },
    { key: '/admin/jobs', icon: <SafetyIcon />, label: '评测与任务' },
    { key: '/admin/model-config', icon: <SettingOutlined />, label: '模型配置' },
    { key: '/admin/hub', icon: <ApiOutlined />, label: 'Hub 管理' },
    { key: '/admin/health', icon: <ApiOutlined />, label: '系统健康' },
  ]

  const selectedKey = menuItems.find((item) => location.pathname.startsWith(item.key))?.key || '/admin/dashboard'

  return (
    <Layout style={{ minHeight: '100vh', background: 'var(--bg-base)' }}>
      <Sider
        width={228}
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
        <div style={{ height: 64, display: 'flex', alignItems: 'center', paddingLeft: 20, borderBottom: '1px solid var(--border-color)', gap: 10 }}>
          <img src={appLogoUrl} alt="logo" style={{ width: 40, height: 40, objectFit: 'contain' }} />
          <div>
            <div style={{ color: 'var(--text-primary)', fontWeight: 700, fontSize: 14 }}>AI 安全评测</div>
            <div style={{ color: 'var(--text-muted)', fontSize: 11 }}>管理员控制台</div>
          </div>
        </div>
        <Menu
          mode="inline"
          selectedKeys={[selectedKey]}
          style={{ background: 'transparent', border: 'none', marginTop: 8 }}
          items={menuItems.map((item) => ({
            key: item.key,
            icon: item.icon,
            label: <Link to={item.key}>{item.label}</Link>,
          }))}
        />
      </Sider>

      <Layout style={{ marginLeft: 228 }}>
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
          <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>maclaw-only 运营治理后台</Text>
          <Dropdown
            menu={{
              items: [{
                key: 'logout',
                icon: <LogoutOutlined />,
                label: '退出',
                onClick: () => {
                  logout()
                  navigate('/login')
                },
              }],
            }}
            placement="bottomRight"
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <Avatar size={32} icon={<UserOutlined />} style={{ background: 'rgba(124, 58, 237, 0.12)', border: '1px solid rgba(124, 58, 237, 0.4)' }} />
              <Text style={{ color: 'var(--text-primary)', fontSize: 13 }}>{user?.name || '管理员'}</Text>
            </div>
          </Dropdown>
        </Header>

        <Content style={{ padding: 24, minHeight: 'calc(100vh - 64px)' }}>
          <Routes>
            <Route path="dashboard" element={<AdminDashboard />} />
            <Route path="users" element={<UserManagement />} />
            <Route path="accounts" element={<AccountTenantManagement />} />
            <Route path="resources" element={<ResourceGovernance />} />
            <Route path="jobs" element={<JobGovernance />} />
            <Route path="model-config" element={<MaclawModelConfig title="平台默认 maclaw 模型配置" />} />
            <Route path="hub" element={<MaclawHubManagement />} />
            <Route path="health" element={<SystemHealth />} />
            <Route path="*" element={<AdminDashboard />} />
          </Routes>
        </Content>
      </Layout>
    </Layout>
  )
}

function SafetyIcon() {
  return <ApiOutlined />
}