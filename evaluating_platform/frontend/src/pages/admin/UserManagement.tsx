import { useEffect, useState } from 'react'
import {
  Button, Popconfirm, Table, Tag, Typography, Input, Select, Space, Switch, message
} from 'antd'
import { DeleteOutlined, SearchOutlined } from '@ant-design/icons'
import { adminService } from '../../services/admin'
import type { User } from '../../services/auth'

const { Title, Text } = Typography

const roleLabel: Record<string, string> = {
  enterprise: '企业客户',
  expert: '安全专家',
  admin: '管理员',
}

const roleColor: Record<string, string> = {
  enterprise: 'blue',
  expert: 'orange',
  admin: 'purple',
}

export function UserManagement() {
  const [users, setUsers] = useState<User[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [keyword, setKeyword] = useState('')
  const [roleFilter, setRoleFilter] = useState('')
  const [page, setPage] = useState(1)

  const pageSize = 20

  const loadUsers = () => {
    setLoading(true)
    adminService.listUsers({
      limit: pageSize,
      offset: (page - 1) * pageSize,
      role: roleFilter || undefined,
      keyword: keyword || undefined,
    }).then(r => {
      setUsers(r.items || [])
      setTotal(r.total || 0)
    }).catch(() => {}).finally(() => setLoading(false))
  }

  useEffect(() => { loadUsers() }, [page, roleFilter])

  const handleRoleChange = async (id: string, role: string) => {
    try {
      await adminService.updateUserRole(id, role)
      message.success('角色已更新')
      loadUsers()
    } catch (err: unknown) {
      message.error((err as Error).message || '更新失败')
    }
  }

  const handleActiveChange = async (id: string, active: boolean) => {
    try {
      await adminService.setUserActive(id, active)
      message.success(active ? '用户已启用' : '用户已禁用')
      loadUsers()
    } catch (err: unknown) {
      message.error((err as Error).message || '操作失败')
    }
  }

  const handleDeleteUser = async (id: string) => {
    try {
      await adminService.deleteUser(id)
      message.success('用户已删除')
      loadUsers()
    } catch (err: unknown) {
      message.error((err as Error).message || '删除失败')
    }
  }

  const columns = [
    {
      title: '邮箱',
      dataIndex: 'email',
      render: (v: string) => <Text style={{ color: 'var(--text-primary)' }}>{v}</Text>,
    },
    {
      title: '姓名',
      dataIndex: 'name',
      render: (v: string) => <Text style={{ color: 'var(--text-secondary)' }}>{v}</Text>,
    },
    {
      title: '机构',
      dataIndex: 'org_name',
      render: (v: string) => <Text style={{ color: 'var(--text-muted)' }}>{v || '—'}</Text>,
    },
    {
      title: '角色',
      dataIndex: 'role',
      render: (v: string, r: User) => (
        <Select
          value={v}
          size="small"
          style={{ width: 110 }}
          onChange={role => handleRoleChange(r.id, role)}
          options={[
            { value: 'enterprise', label: '企业客户' },
            { value: 'expert', label: '安全专家' },
            { value: 'admin', label: '管理员' },
          ]}
        />
      ),
    },
    {
      title: '状态',
      dataIndex: 'is_active',
      render: (v: boolean, r: User) => (
        <Switch
          checked={v}
          size="small"
          checkedChildren="启用"
          unCheckedChildren="禁用"
          onChange={active => handleActiveChange(r.id, active)}
        />
      ),
    },
    {
      title: '余额',
      dataIndex: 'balance',
      render: (v: number) => <Text style={{ color: 'var(--color-primary)' }}>¥{v?.toFixed(2)}</Text>,
    },
    {
      title: '注册时间',
      dataIndex: 'created_at',
      render: (v: string) => <Text style={{ color: 'var(--text-muted)', fontSize: 12 }}>{v?.slice(0, 10)}</Text>,
    },
    {
      title: '操作',
      render: (_: unknown, r: User) => (
        <Popconfirm
          title="删除用户"
          description="将删除该平台账号，并级联清理其 maclaw 映射和资源索引。此操作不可撤销。"
          okText="删除"
          cancelText="取消"
          okButtonProps={{ danger: true }}
          onConfirm={() => handleDeleteUser(r.id)}
        >
          <Button danger size="small" icon={<DeleteOutlined />}>删除</Button>
        </Popconfirm>
      ),
    },
  ]

  return (
    <div>
      <div style={{ marginBottom: 24 }}>
        <Title level={4} style={{ color: 'var(--text-primary)', margin: 0 }}>用户管理</Title>
        <Text style={{ color: 'var(--text-secondary)', fontSize: 13 }}>管理平台所有用户账户</Text>
      </div>

      {/* 角色分布 */}
      <div style={{ display: 'flex', gap: 12, marginBottom: 20 }}>
        {Object.entries(roleLabel).map(([role, label]) => (
          <Tag key={role} color={roleColor[role]} style={{ padding: '4px 12px', fontSize: 13 }}>
            {label}
          </Tag>
        ))}
      </div>

      <Space style={{ marginBottom: 20 }}>
        <Input
          placeholder="搜索邮箱或姓名..."
          prefix={<SearchOutlined style={{ color: 'var(--text-muted)' }} />}
          style={{ width: 260 }}
          value={keyword}
          onChange={e => setKeyword(e.target.value)}
          onPressEnter={loadUsers}
        />
        <Select
          value={roleFilter || 'all'}
          style={{ width: 130 }}
          onChange={v => { setRoleFilter(v === 'all' ? '' : v); setPage(1) }}
          options={[
            { value: 'all', label: '全部角色' },
            { value: 'enterprise', label: '企业客户' },
            { value: 'expert', label: '安全专家' },
            { value: 'admin', label: '管理员' },
          ]}
        />
      </Space>

      <Table
        dataSource={users}
        columns={columns}
        rowKey="id"
        loading={loading}
        pagination={{
          current: page,
          pageSize,
          total,
          onChange: setPage,
          showTotal: t => `共 ${t} 条`,
        }}
        style={{ background: 'transparent' }}
      />
    </div>
  )
}