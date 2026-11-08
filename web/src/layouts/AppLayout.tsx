import { Layout, Menu, Grid, Typography, theme } from 'antd'
import { AppstoreOutlined, CloudServerOutlined, HistoryOutlined, SettingOutlined } from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router'

const items = [
  { key: '/', icon: <AppstoreOutlined />, label: '更新' },
  { key: '/hosts', icon: <CloudServerOutlined />, label: '设备' },
  { key: '/history', icon: <HistoryOutlined />, label: '检测历史' },
  { key: '/settings', icon: <SettingOutlined />, label: '设置' },
]

export default function AppLayout() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const screens = Grid.useBreakpoint()
  const { token } = theme.useToken()
  const selected = items.find((i) => i.key !== '/' && pathname.startsWith(i.key))?.key ?? '/'

  return (
    <Layout style={{ minHeight: '100vh', background: 'transparent' }}>
      <Layout.Header
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 24,
          background: token.colorBgContainer,
          borderBottom: `1px solid ${token.colorBorderSecondary}`,
          paddingInline: screens.md ? 32 : 16,
        }}
      >
        <Typography.Title level={4} style={{ margin: 0, whiteSpace: 'nowrap' }}>
          DKStore
        </Typography.Title>
        <Menu
          mode="horizontal"
          selectedKeys={[selected]}
          items={items}
          onClick={(e) => navigate(e.key)}
          style={{ flex: 1, minWidth: 0, borderBottom: 'none', background: 'transparent' }}
        />
      </Layout.Header>
      <Layout.Content style={{ width: '100%', maxWidth: 1080, margin: '0 auto', padding: screens.md ? 32 : 16 }}>
        <Outlet />
      </Layout.Content>
    </Layout>
  )
}
