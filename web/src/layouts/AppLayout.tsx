import { useEffect, useState } from 'react'
import { Button, Card, Flex, Grid, Input, Layout, Menu, Segmented, Space, Spin, Typography, message, theme } from 'antd'
import {
  AppstoreOutlined,
  CloudServerOutlined,
  ContainerOutlined,
  GlobalOutlined,
  HistoryOutlined,
  LockOutlined,
  ReloadOutlined,
  SettingOutlined,
  SafetyCertificateOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api } from '../api/client'
import { useEvents } from '../hooks/useEvents'
import { useI18n } from '../i18n'
import SetupPage from '../pages/Setup'
import type { Locale } from '../store/ui'

export default function AppLayout() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const screens = Grid.useBreakpoint()
  const { token } = theme.useToken()
  const { locale, setLocale, t } = useI18n()
  const qc = useQueryClient()

  const auth = useQuery({ queryKey: ['authStatus'], queryFn: api.authStatus })
  const [pw, setPw] = useState('')
  const [loggingIn, setLoggingIn] = useState(false)
  const [inSetupStep2, setInSetupStep2] = useState(false)

  const canSubscribeEvents =
    Boolean(auth.data) &&
    auth.data?.mode !== 'setup_required' &&
    auth.data?.mode !== 'host_login_required' &&
    (auth.data?.mode !== 'password' || auth.data.authenticated)
  useEvents(canSubscribeEvents)

  // 任一业务请求返回 401（会话过期 / 需要初始化 / 设备登录态失效）时重新获取访问状态，切换到对应页面
  useEffect(() => {
    return qc.getQueryCache().subscribe((ev) => {
      const err = ev.query.state.error
      if (ev.type === 'updated' && err instanceof ApiError && err.status === 401 && ev.query.queryKey[0] !== 'authStatus') {
        qc.invalidateQueries({ queryKey: ['authStatus'] })
      }
    })
  }, [qc])

  const items = [
    { key: '/', icon: <AppstoreOutlined />, label: t.nav.updates },
    { key: '/containers', icon: <ContainerOutlined />, label: t.nav.containers },
    { key: '/hosts', icon: <CloudServerOutlined />, label: t.nav.hosts },
    { key: '/history', icon: <HistoryOutlined />, label: t.nav.history },
    { key: '/settings', icon: <SettingOutlined />, label: t.nav.settings },
  ]
  const selected = items.find((i) => i.key !== '/' && pathname.startsWith(i.key))?.key ?? '/'

  const onSwitchLang = async (next: Locale) => {
    setLocale(next)
    try {
      const cfg = await api.getSettings()
      if (cfg.locale !== next) {
        await api.saveSettings({ ...cfg, locale: next })
      }
    } catch {
      // 忽略未登录状态下的保存失败
    }
  }

  const doLogin = async () => {
    setLoggingIn(true)
    try {
      await api.login(pw)
      setPw('')
      await qc.invalidateQueries()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoggingIn(false)
    }
  }

  if (auth.isPending && !inSetupStep2) {
    return (
      <Flex align="center" justify="center" style={{ minHeight: '100vh' }}>
        <Spin />
      </Flex>
    )
  }

  if (auth.data?.mode === 'host_login_required' && !inSetupStep2) {
    return (
      <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
        <Card style={{ width: 440 }}>
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Flex justify="space-between" align="center">
              <Typography.Title level={4} style={{ margin: 0 }}>
                <SafetyCertificateOutlined /> {t.auth.hostLoginTitle}
              </Typography.Title>
              <Segmented
                size="small"
                value={locale}
                onChange={(v) => setLocale(v as Locale)}
                options={[
                  { label: '中文', value: 'zh-CN' },
                  { label: 'EN', value: 'en-US' },
                ]}
              />
            </Flex>
            <Typography.Paragraph type="secondary" style={{ marginBottom: 4 }}>
              {t.auth.hostLoginDesc}
            </Typography.Paragraph>
            <Button
              type="primary"
              block
              icon={<ReloadOutlined />}
              loading={auth.isFetching}
              onClick={() => auth.refetch()}
            >
              {t.auth.hostLoginRetry}
            </Button>
          </Space>
        </Card>
      </Flex>
    )
  }

  if (auth.data?.mode === 'setup_required' || inSetupStep2) {
    return (
      <SetupPage
        authStatus={auth.data}
        onStepChange={setInSetupStep2}
        onDone={() => {
          setInSetupStep2(false)
          qc.invalidateQueries()
        }}
      />
    )
  }

  if (auth.data?.mode === 'password' && !auth.data.authenticated) {
    return (
      <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
        <Card style={{ width: 380 }}>
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            <Flex justify="space-between" align="center">
              <Typography.Title level={4} style={{ margin: 0 }}>
                <LockOutlined /> DKStore
              </Typography.Title>
              <Segmented
                size="small"
                value={locale}
                onChange={(v) => setLocale(v as Locale)}
                options={[
                  { label: '中文', value: 'zh-CN' },
                  { label: 'EN', value: 'en-US' },
                ]}
              />
            </Flex>
            <Typography.Text type="secondary">{t.settings.loginTitle}</Typography.Text>
            <Input.Password
              value={pw}
              onChange={(e) => setPw(e.target.value)}
              onPressEnter={doLogin}
            />
            <Button type="primary" block loading={loggingIn} onClick={doLogin}>
              {t.settings.loginBtn}
            </Button>
          </Space>
        </Card>
      </Flex>
    )
  }

  return (
    <Layout style={{ minHeight: '100vh', background: 'transparent' }}>
      <Layout.Header
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 16,
          background: token.colorBgContainer,
          borderBottom: `1px solid ${token.colorBorderSecondary}`,
          paddingInline: screens.md ? 32 : 12,
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
        <Space size={8}>
          <Segmented
            size="small"
            value={locale}
            onChange={(v) => onSwitchLang(v as Locale)}
            options={[
              { label: screens.sm ? '简体中文' : '中', value: 'zh-CN', icon: <GlobalOutlined /> },
              { label: 'EN', value: 'en-US' },
            ]}
          />
          {auth.data?.mode === 'password' && (
            <Button
              size="small"
              onClick={async () => {
                await api.logout()
                qc.invalidateQueries()
              }}
            >
              {t.nav.logout}
            </Button>
          )}
        </Space>
      </Layout.Header>
      <Layout.Content
        style={{
          width: '100%',
          maxWidth: 1120,
          margin: '0 auto',
          padding: screens.md ? 32 : 16,
        }}
      >
        <Outlet />
      </Layout.Content>
    </Layout>
  )
}
