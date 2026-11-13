import { useEffect, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Flex,
  Grid,
  Input,
  Layout,
  Menu,
  Popover,
  Segmented,
  Space,
  Spin,
  Typography,
  message,
  theme,
} from 'antd'
import {
  AppstoreOutlined,
  CheckCircleFilled,
  CloseCircleFilled,
  CloudServerOutlined,
  ContainerOutlined,
  HistoryOutlined,
  LockOutlined,
  ReloadOutlined,
  SettingOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, api, onUnauthorized, type AuthStatus } from '../api/client'
import AuthToolbar from '../components/AuthToolbar'
import DKStoreLogo from '../components/DKStoreLogo'
import UpdateLogModal from '../components/UpdateLogModal'
import UpdateQueuePanel from '../components/UpdateQueuePanel'
import { useEffectiveDark } from '../hooks/useEffectiveDark'
import { useEvents } from '../hooks/useEvents'
import { useIsUltrawide, useOrderedQueueJobs } from '../hooks/useUpdateQueue'
import { useI18n } from '../i18n'
import SetupPage from '../pages/Setup'
import { useUIStore, type Locale } from '../store/ui'

const AUTH_EXPIRED_MSG_KEY = 'dkstore-auth-expired'

export default function AppLayout() {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const screens = Grid.useBreakpoint()
  const { token } = theme.useToken()
  const { locale, setLocale, t } = useI18n()
  const isDark = useEffectiveDark()
  const qc = useQueryClient()

  const auth = useQuery({ queryKey: ['authStatus'], queryFn: api.authStatus })
  const [pw, setPw] = useState('')
  const [loggingIn, setLoggingIn] = useState(false)
  const [inSetupStep2, setInSetupStep2] = useState(false)
  const [skippedLocal, setSkippedLocal] = useState(
    () => typeof window !== 'undefined' && localStorage.getItem('dkstore_skipped_local') === '1',
  )
  const [dismissedRemind, setDismissedRemind] = useState(
    () => typeof window !== 'undefined' && localStorage.getItem('dkstore_dismiss_local_remind') === '1',
  )

  const isUltrawide = useIsUltrawide()
  const queueJobs = useOrderedQueueJobs()
  const queueCollapsed = useUIStore((s) => s.queueCollapsed)
  const toggleQueueCollapsed = useUIStore((s) => s.toggleQueueCollapsed)
  const [popoverOpen, setPopoverOpen] = useState(false)
  const hydratedProgressStyleRef = useRef(false)
  const wasAuthenticatedRef = useRef(false)
  const manualLogoutRef = useRef(false)
  const canSubscribeEventsRef = useRef(false)
  const tRef = useRef(t)

  const canSubscribeEvents =
    Boolean(auth.data) &&
    auth.data?.mode !== 'setup_required' &&
    auth.data?.mode !== 'host_login_required' &&
    (auth.data?.mode !== 'password' || auth.data.authenticated)
  useEvents(canSubscribeEvents)

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: api.getSettings,
    enabled: canSubscribeEvents,
  })

  // 同步后端保存的 progressStyle（默认 layered）
  useEffect(() => {
    if (!hydratedProgressStyleRef.current && settingsQuery.data?.progressStyle) {
      const s = settingsQuery.data.progressStyle
      if (s === 'layered' || s === 'unified') {
        hydratedProgressStyleRef.current = true
        useUIStore.getState().setProgressStyle(s)
      }
    }
  }, [settingsQuery.data?.progressStyle])

  // 页面首次加载时若后台已有正在排队/执行中的升级任务，立即恢复到更新队列中
  useEffect(() => {
    if (!canSubscribeEvents) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.listUpdates()
        if (cancelled || !res.items) return
        for (const job of res.items) {
          if (job.status === 'queued' || job.status === 'running') {
            useUIStore.getState().upsertUpdateJob(job)
          }
        }
      } catch {
        // ignore
      }
    })()
    return () => {
      cancelled = true
    }
  }, [canSubscribeEvents])

  const hostsQuery = useQuery({
    queryKey: ['hosts', locale],
    queryFn: api.listHosts,
    enabled: canSubscribeEvents,
  })

  // 预加载本地已收录的容器快照并在后台静默预热实时状态，确保首次切换到「容器」页时零等待直出
  useEffect(() => {
    if (!canSubscribeEvents) return
    void qc.prefetchQuery({
      queryKey: ['containers-cached', ''],
      queryFn: () => api.listContainers(undefined, { cached: true }),
      staleTime: 30000,
    })
    const timer = setTimeout(() => {
      void qc.prefetchQuery({
        queryKey: ['containers', ''],
        queryFn: () => api.listContainers(undefined, { withStats: true }),
        staleTime: 10000,
      })
    }, 300)
    return () => clearTimeout(timer)
  }, [canSubscribeEvents, qc])

  useEffect(() => {
    tRef.current = t
  }, [t])

  // 记录已登录状态；当从已登录非主动退回未登录（后台重启、SSE 断连探测、切回标签页重验）时，弹出唯一掉线提醒
  useEffect(() => {
    canSubscribeEventsRef.current = canSubscribeEvents
    if (canSubscribeEvents) {
      wasAuthenticatedRef.current = true
      message.destroy(AUTH_EXPIRED_MSG_KEY)
      return
    }
    if (auth.data && wasAuthenticatedRef.current && !manualLogoutRef.current) {
      wasAuthenticatedRef.current = false
      message.warning({
        key: AUTH_EXPIRED_MSG_KEY,
        content: tRef.current.auth.sessionExpired,
      })
    }
  }, [auth.data, canSubscribeEvents])

  // 任一业务请求（含直接调用的 api.* 与 React Query）返回 401 时，立即提示掉线并切回对应登录/初始化页
  useEffect(() => {
    const handleAuthExpired = (err: ApiError) => {
      if (manualLogoutRef.current) return
      if (wasAuthenticatedRef.current || canSubscribeEventsRef.current) {
        wasAuthenticatedRef.current = false
        message.warning({
          key: AUTH_EXPIRED_MSG_KEY,
          content: tRef.current.auth.sessionExpired,
        })
      }
      qc.setQueryData<AuthStatus>(['authStatus'], (prev) => {
        if (!prev) return prev
        if (err.code === 'setup_required') {
          return { ...prev, mode: 'setup_required', authenticated: false }
        }
        return {
          ...prev,
          mode: prev.mode === 'device' ? 'password' : prev.mode,
          authenticated: false,
        }
      })
      void qc.invalidateQueries({ queryKey: ['authStatus'] })
    }

    const unsubApi = onUnauthorized(handleAuthExpired)
    const unsubCache = qc.getQueryCache().subscribe((ev) => {
      const err = ev.query.state.error
      if (
        ev.type === 'updated' &&
        err instanceof ApiError &&
        err.status === 401 &&
        err.code !== 'invalid_credentials' &&
        ev.query.queryKey[0] !== 'authStatus'
      ) {
        handleAuthExpired(err)
      }
    })
    return () => {
      unsubApi()
      unsubCache()
    }
  }, [qc])

  // 切回浏览器标签页或窗口获焦时静默核验一次登录态，无需等手动点击才发现掉线
  useEffect(() => {
    if (!canSubscribeEvents) return
    let lastCheckedAt = 0
    const checkNow = () => {
      if (document.visibilityState === 'hidden') return
      const now = Date.now()
      if (now - lastCheckedAt < 3000) return
      lastCheckedAt = now
      void qc.invalidateQueries({ queryKey: ['authStatus'] })
    }
    document.addEventListener('visibilitychange', checkNow)
    window.addEventListener('focus', checkNow)
    return () => {
      document.removeEventListener('visibilitychange', checkNow)
      window.removeEventListener('focus', checkNow)
    }
  }, [canSubscribeEvents, qc])

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
      <Flex className="dk-aurora-bg" align="center" justify="center" style={{ minHeight: '100vh' }}>
        <Spin />
      </Flex>
    )
  }

  if (auth.data?.mode === 'host_login_required' && !inSetupStep2) {
    return (
      <Flex
        vertical
        justify="space-between"
        className="dk-aurora-bg"
        style={{ minHeight: '100vh', padding: 20 }}
      >
        <Flex justify="flex-end">
          <AuthToolbar />
        </Flex>
        <Flex align="center" justify="center" style={{ flex: 1, paddingBlock: 16 }}>
          <div
            className="dk-glass-card"
            style={{
              width: '100%',
              maxWidth: 420,
              borderRadius: 20,
              padding: '34px 32px',
            }}
          >
            <Flex vertical align="center" style={{ textAlign: 'center', marginBottom: 22 }}>
              <DKStoreLogo size={54} withHalo dark={isDark} />
              <div
                style={{
                  fontSize: 22,
                  fontWeight: 700,
                  letterSpacing: '-0.02em',
                  color: token.colorText,
                  marginTop: 14,
                }}
              >
                DKStore
              </div>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM, marginTop: 2 }}>
                {t.auth.tagline}
              </Typography.Text>
            </Flex>
            <div style={{ textAlign: 'center', marginBottom: 20 }}>
              <Typography.Title level={5} style={{ margin: '0 0 8px' }}>
                {t.auth.hostLoginTitle}
              </Typography.Title>
              <Typography.Paragraph
                type="secondary"
                style={{ fontSize: token.fontSizeSM, marginBottom: 0, lineHeight: 1.65 }}
              >
                {auth.data.deviceAuthHint || t.auth.hostLoginDesc}
              </Typography.Paragraph>
            </div>
            <Button
              type="primary"
              size="large"
              block
              icon={<ReloadOutlined />}
              loading={auth.isFetching}
              onClick={() => auth.refetch()}
              style={{ height: 42, borderRadius: 10, fontWeight: 500 }}
            >
              {t.auth.hostLoginRetry}
            </Button>
          </div>
        </Flex>
        <div
          style={{
            textAlign: 'center',
            fontSize: token.fontSizeSM,
            color: token.colorTextTertiary,
            paddingBottom: 4,
          }}
        >
          {t.auth.platformsFooter}
        </div>
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
          setSkippedLocal(localStorage.getItem('dkstore_skipped_local') === '1')
          setDismissedRemind(localStorage.getItem('dkstore_dismiss_local_remind') === '1')
          qc.invalidateQueries()
        }}
      />
    )
  }

  if (auth.data?.mode === 'password' && !auth.data.authenticated) {
    return (
      <Flex
        vertical
        justify="space-between"
        className="dk-aurora-bg"
        style={{ minHeight: '100vh', padding: 20 }}
      >
        <Flex justify="flex-end">
          <AuthToolbar />
        </Flex>
        <Flex align="center" justify="center" style={{ flex: 1, paddingBlock: 16 }}>
          <div
            className="dk-glass-card"
            style={{
              width: '100%',
              maxWidth: 400,
              borderRadius: 20,
              padding: '34px 32px',
            }}
          >
            <Flex vertical align="center" style={{ textAlign: 'center', marginBottom: 24 }}>
              <DKStoreLogo size={54} withHalo dark={isDark} />
              <div
                style={{
                  fontSize: 22,
                  fontWeight: 700,
                  letterSpacing: '-0.02em',
                  color: token.colorText,
                  marginTop: 14,
                }}
              >
                DKStore
              </div>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM, marginTop: 2 }}>
                {t.auth.tagline}
              </Typography.Text>
            </Flex>
            <Space direction="vertical" size={14} style={{ width: '100%' }}>
              <Input.Password
                size="large"
                prefix={<LockOutlined style={{ color: token.colorTextQuaternary }} />}
                placeholder={t.settings.loginTitle}
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                onPressEnter={doLogin}
                autoFocus
                style={{ borderRadius: 10 }}
              />
              <Button
                type="primary"
                size="large"
                block
                loading={loggingIn}
                onClick={doLogin}
                style={{
                  height: 42,
                  borderRadius: 10,
                  fontWeight: 500,
                }}
              >
                {t.settings.loginBtn}
              </Button>
            </Space>
          </div>
        </Flex>
        <div
          style={{
            textAlign: 'center',
            fontSize: token.fontSizeSM,
            color: token.colorTextTertiary,
            paddingBottom: 4,
          }}
        >
          {t.auth.platformsFooter}
        </div>
      </Flex>
    )
  }

  const hostItems = hostsQuery.data?.items ?? []
  const hasLocalInHosts =
    Boolean(hostsQuery.data?.hasLocalHost) ||
    hostItems.some((h) => {
      if (h.transport === 'local') return true
      const addr = (h.address || '').trim().toLowerCase()
      if (addr === '127.0.0.1' || addr === 'localhost' || addr === '::1') return true
      const url = (h.dsmUrl || '').trim().toLowerCase()
      return url.includes('//127.0.0.1') || url.includes('//localhost') || url.includes('//[::1]')
    })
  const hasLocalHost = auth.data?.hasLocalHost !== false || hasLocalInHosts
  const showLocalRemind =
    Boolean(hostsQuery.data) &&
    (auth.data?.installMode !== 'docker' || skippedLocal) &&
    !hasLocalHost &&
    !dismissedRemind

  const anyQueueRunning = queueJobs.some((j) => j.status === 'queued' || j.status === 'running')
  const hasFailed = queueJobs.some((j) => j.status === 'failed')
  const isHomeRoute = pathname === '/'
  const useHeaderPopover = !isUltrawide && !isHomeRoute

  const queuePillBtn =
    queueJobs.length > 0 ? (
      <Button
        size="small"
        type={anyQueueRunning ? 'primary' : 'default'}
        ghost={anyQueueRunning}
        icon={
          anyQueueRunning ? (
            <SyncOutlined spin />
          ) : hasFailed ? (
            <CloseCircleFilled style={{ color: token.colorError }} />
          ) : (
            <CheckCircleFilled style={{ color: token.colorSuccess }} />
          )
        }
        onClick={() => {
          if (useHeaderPopover) {
            if (popoverOpen) {
              useUIStore.getState().clearFinishedQueueJobs()
            }
            setPopoverOpen((v) => !v)
          } else {
            if (!queueCollapsed) {
              useUIStore.getState().clearFinishedQueueJobs()
            }
            toggleQueueCollapsed()
          }
        }}
      >
        {t.queue.headerPill.replace('{count}', String(queueJobs.length))}
      </Button>
    ) : null

  return (
    <Layout style={{ minHeight: '100vh', background: 'transparent' }}>
      <Layout.Header
        style={{
          padding: 0,
          height: 60,
          lineHeight: 'normal',
          background: token.colorBgContainer,
          borderBottom: '1px solid ' + token.colorBorderSecondary,
          display: 'flex',
          alignItems: 'center',
        }}
      >
        <div
          style={{
            width: '100%',
            maxWidth: 1280,
            margin: '0 auto',
            paddingInline: screens.md ? 32 : 16,
            display: 'flex',
            alignItems: 'center',
            gap: 16,
          }}
        >
          <div
            onClick={() => navigate('/')}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              cursor: 'pointer',
              userSelect: 'none',
              flexShrink: 0,
            }}
          >
            <DKStoreLogo size={28} />
            <Typography.Title
              level={4}
              style={{ margin: 0, whiteSpace: 'nowrap', letterSpacing: '-0.02em' }}
            >
              DKStore
            </Typography.Title>
          </div>
          <Menu
            mode="horizontal"
            selectedKeys={[selected]}
            items={items}
            onClick={(e) => navigate(e.key)}
            style={{
              flex: 1,
              minWidth: 0,
              borderBottom: 'none',
              background: 'transparent',
              lineHeight: '58px',
            }}
          />
          <Space size={8} align="center">
            {queuePillBtn &&
              (useHeaderPopover ? (
                <Popover
                  open={popoverOpen && queueJobs.length > 0}
                  onOpenChange={(nextOpen) => {
                    if (!nextOpen) {
                      useUIStore.getState().clearFinishedQueueJobs()
                    }
                    setPopoverOpen(nextOpen)
                  }}
                  trigger="click"
                  placement="bottomRight"
                  content={
                    <div style={{ width: 376, maxWidth: 'calc(100vw - 40px)' }}>
                      <UpdateQueuePanel mode="popover" onCollapse={() => setPopoverOpen(false)} />
                    </div>
                  }
                >
                  {queuePillBtn}
                </Popover>
              ) : (
                queuePillBtn
              ))}
            <Segmented
              size="small"
              value={locale}
              onChange={(v) => onSwitchLang(v as Locale)}
              options={[
                { label: '中文', value: 'zh-CN' },
                { label: 'EN', value: 'en-US' },
              ]}
            />
            {auth.data?.mode === 'password' && (
              <Button
                size="small"
                onClick={async () => {
                  manualLogoutRef.current = true
                  wasAuthenticatedRef.current = false
                  try {
                    await api.logout()
                    qc.setQueryData(['authStatus'], (prev: typeof auth.data) =>
                      prev ? { ...prev, authenticated: false } : prev,
                    )
                    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'authStatus' })
                    await qc.invalidateQueries({ queryKey: ['authStatus'] })
                  } finally {
                    manualLogoutRef.current = false
                  }
                }}
              >
                {t.nav.logout}
              </Button>
            )}
          </Space>
        </div>
      </Layout.Header>

      <Layout.Content
        style={{
          maxWidth: 1280,
          width: '100%',
          margin: '0 auto',
          padding: screens.md ? '24px 32px 40px' : 16,
        }}
      >
        {showLocalRemind && (
          <Alert
            type="info"
            showIcon
            closable={{
              onClose: () => {
                localStorage.setItem('dkstore_dismiss_local_remind', '1')
                setDismissedRemind(true)
              },
            }}
            title={t.auth.localRemindText}
            action={
              <Button
                size="small"
                type="link"
                onClick={() => navigate('/hosts?connectLocal=1')}
              >
                {t.auth.localRemindConnectBtn}
              </Button>
            }
            style={{ marginBottom: 16 }}
          />
        )}
        <Outlet />
      </Layout.Content>

      {/* 超宽屏（>= 2020px）停靠在右侧纯空白边距区域：0 遮挡、0 挤压卡片 */}
      {isUltrawide && queueJobs.length > 0 && !queueCollapsed && (
        <div
          style={{
            position: 'fixed',
            top: 80,
            right: 'max(16px, calc((100vw - 1280px) / 2 - 392px))',
            width: 376,
            zIndex: 90,
          }}
        >
          <UpdateQueuePanel mode="docked" />
        </div>
      )}

      {/* 全局只读 xterm.js 实时原始日志终端弹窗 */}
      <UpdateLogModal />
    </Layout>
  )
}
