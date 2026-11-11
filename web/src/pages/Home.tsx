import { useEffect, useMemo, useState } from 'react'
import { Button, Card, Col, Empty, Input, Modal, Row, Segmented, Spin, message, theme } from 'antd'
import { PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, type AppCardItem, type FailedItem } from '../api/client'
import AppCard from '../components/AppCard'
import UpdateQueuePanel from '../components/UpdateQueuePanel'
import { useIsUltrawide, useOrderedQueueJobs } from '../hooks/useUpdateQueue'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

export default function HomePage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const navigate = useNavigate()

  const [hostFilter, setHostFilter] = useState<string>('')
  const liveProgress = useUIStore((s) => s.liveProgress)
  const setLiveProgress = useUIStore((s) => s.setLiveProgress)
  const queueCollapsed = useUIStore((s) => s.queueCollapsed)
  const isUltrawide = useIsUltrawide()
  const queueJobs = useOrderedQueueJobs()

  // 分组折叠状态：默认展开可更新、需谨慎更新（非空时自动展开）与无需更新
  const [normalExpanded, setNormalExpanded] = useState(true)
  const [cautionToggled, setCautionToggled] = useState<boolean | null>(null)
  const [noUpdateExpanded, setNoUpdateExpanded] = useState(true)
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))

  // Docker Hub 快捷登录弹窗状态
  const [hubModalOpen, setHubModalOpen] = useState(false)
  const [hubUser, setHubUser] = useState('')
  const [hubSecret, setHubSecret] = useState('')
  const [hubSubmitting, setHubSubmitting] = useState(false)

  // 其他错误提示条展开状态
  const [failedExpanded, setFailedExpanded] = useState(false)

  const overview = useQuery({
    queryKey: ['overview', hostFilter, locale],
    queryFn: () => api.overview(hostFilter || undefined),
  })

  const progress = liveProgress ?? overview.data?.progress
  const isRunning = progress?.running ?? false

  const normalList = overview.data?.normal ?? []
  const cautionList = overview.data?.caution ?? []
  const noUpdateList = overview.data?.noUpdate ?? overview.data?.ignored ?? []
  const updateCount = normalList.length + cautionList.length

  useEffect(() => {
    document.title = updateCount > 0 ? `(${updateCount}) DKStore` : 'DKStore'
  }, [updateCount])

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 30000)
    return () => clearInterval(id)
  }, [])

  const cautionExpanded = cautionToggled ?? cautionList.length > 0

  const completedKeySet = useMemo(
    () => new Set(progress?.completedServiceKeys ?? []),
    [progress?.completedServiceKeys],
  )

  const isCardChecking = (item: AppCardItem): boolean => {
    if (!isRunning) return false
    if (progress?.phase === 'discovering') return true
    if (progress?.phase === 'checking') {
      if (Array.isArray(progress.completedServiceKeys)) {
        const checkSvcs = item.services.length > 0 ? item.services : (item.allServices ?? [])
        if (checkSvcs.length === 0) return false
        return !checkSvcs.every((s) => completedKeySet.has(s.key))
      }
      if (progress.currentServiceKey) {
        return item.services.some((s) => s.key === progress.currentServiceKey)
      }
    }
    return false
  }

  const handleCheck = async () => {
    try {
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const handleSaveHubAndRecheck = async () => {
    const username = hubUser.trim()
    const secret = hubSecret.trim()
    if (!username || !secret) {
      message.warning(t.home.hubRequiredMsg)
      return
    }
    setHubSubmitting(true)
    try {
      await api.saveRegCred({ registry: 'docker.io', username, secret })
      qc.invalidateQueries({ queryKey: ['regCreds'] })
      setHubModalOpen(false)
      setHubSecret('')
      message.success(t.home.hubSavedRechecking)
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setHubSubmitting(false)
    }
  }

  const formatLastChecked = (ts?: number): string => {
    if (!ts) return t.home.neverChecked
    const baseSec = overview.dataUpdatedAt
      ? Math.max(nowSec, Math.floor(overview.dataUpdatedAt / 1000))
      : nowSec
    const diffSec = Math.max(0, baseSec - ts)
    if (diffSec < 60) {
      return `${t.home.lastChecked}${t.home.justNow}`
    }
    if (diffSec < 3600) {
      return `${t.home.lastChecked}${t.home.minsAgo.replace('{m}', String(Math.floor(diffSec / 60)))}`
    }
    if (diffSec < 86400) {
      return `${t.home.lastChecked}${t.home.hoursAgo.replace('{h}', String(Math.floor(diffSec / 3600)))}`
    }
    return `${t.home.lastChecked}${new Date(ts * 1000).toLocaleString(locale)}`
  }

  const renderHeaderSubtitle = (): string => {
    if (isRunning) {
      if (progress?.phase === 'discovering') {
        return progress.hostName
          ? `${t.home.discoveringHost}${progress.hostName}`
          : t.home.discoveringGeneric
      }
      if ((progress?.total ?? 0) > 0) {
        return t.home.checkingProgressWithCount
          .replace('{completed}', String(progress?.completed ?? 0))
          .replace('{total}', String(progress?.total ?? 0))
      }
      return `${t.home.checkingProgress}…`
    }

    const total = overview.data?.totalAppCount ?? 0
    const upToDate = overview.data?.upToDateCount ?? 0
    const timePart = formatLastChecked(overview.data?.lastCheckedAt)
    if (total > 0) {
      return `${timePart} · ${t.home.summaryCounts
        .replace('{total}', String(total))
        .replace('{upToDate}', String(upToDate))}`
    }
    return timePart
  }

  const hosts = overview.data?.hosts ?? []
  const rawFailed = overview.data?.failed

  const rateLimitedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind === 'rate_limited'),
    [rawFailed],
  )
  const otherFailedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind !== 'rate_limited'),
    [rawFailed],
  )

  const formatFailedItemLine = (f: FailedItem) => {
    const displayName = f.serviceName && f.serviceName !== 'main' ? f.serviceName : f.appName
    const hostPart = t.home.failedHostWrap.replace('{host}', f.hostName)
    if (f.errorKind === 'auth_required') {
      return (
        <span>
          <strong>{displayName}</strong>
          {hostPart}
          {t.home.failedAuthReason}
        </span>
      )
    }
    return (
      <span>
        <strong>{displayName}</strong>
        {hostPart}— {f.errorMsg}
      </span>
    )
  }

  const renderGrid = (items: AppCardItem[], emptyText: string) => {
    if (items.length === 0) {
      return (
        <div
          style={{
            borderRadius: 14,
            border: `1px dashed ${token.colorBorderSecondary}`,
            padding: '20px 16px',
            textAlign: 'center',
            fontSize: 12,
            color: token.colorTextSecondary,
          }}
        >
          {isRunning ? t.home.checkingGroupEmpty : emptyText}
        </div>
      )
    }
    return (
      <Row gutter={[14, 14]} align="stretch">
        {items.map((item) => (
          <Col
            key={`${item.group}-${item.key}`}
            xs={24}
            md={12}
            lg={8}
            style={{ display: 'flex' }}
          >
            <div style={{ width: '100%' }}>
              <AppCard
                app={item}
                isChecking={isCardChecking(item)}
                onChanged={() => qc.invalidateQueries({ queryKey: ['overview'] })}
              />
            </div>
          </Col>
        ))}
      </Row>
    )
  }

  const renderGroupSection = (
    title: string,
    count: number,
    badgeBg: string,
    badgeFg: string,
    expanded: boolean,
    onToggle: () => void,
    content: React.ReactNode,
  ) => (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div
        onClick={onToggle}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 8,
          cursor: 'pointer',
          userSelect: 'none',
          alignSelf: 'flex-start',
          paddingTop: 4,
        }}
      >
        <span style={{ fontSize: 12, color: token.colorTextSecondary, width: 10 }}>
          {expanded ? '▾' : '▸'}
        </span>
        <span style={{ fontWeight: 600, fontSize: 15, color: token.colorText }}>{title}</span>
        <span
          style={{
            padding: '1px 8px',
            borderRadius: 999,
            background: badgeBg,
            color: badgeFg,
            fontSize: 12,
            fontWeight: 600,
          }}
        >
          {count}
        </span>
      </div>
      {expanded && content}
    </div>
  )

  if (!overview.isPending && hosts.length === 0) {
    return (
      <Card style={{ textAlign: 'center', padding: '48px 16px' }}>
        <Empty description={t.home.emptyNoHosts}>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/hosts')}>
            {t.home.emptyNoHostsBtn}
          </Button>
        </Empty>
      </Card>
    )
  }

  const totalLoadedCards = normalList.length + cautionList.length + noUpdateList.length
  const isInitialScanning = isRunning && totalLoadedCards === 0

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 18, width: '100%' }}>
      {/* 顶栏标题与检查按钮 */}
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 12,
        }}
      >
        <div>
          <h2
            style={{
              margin: 0,
              fontSize: 20,
              fontWeight: 700,
              color: token.colorText,
            }}
          >
            {t.home.title}
          </h2>
          <p
            style={{
              margin: '4px 0 0',
              fontSize: 12,
              color: token.colorTextSecondary,
            }}
          >
            {renderHeaderSubtitle()}
          </p>
        </div>

        <Button
          type="primary"
          icon={<SyncOutlined spin={isRunning} />}
          disabled={isRunning}
          onClick={handleCheck}
          style={{ borderRadius: 10, height: 36, paddingInline: 16, fontWeight: 500 }}
        >
          {isRunning ? t.home.checking : t.home.checkNow}
        </Button>
      </div>

      {/* 中小屏（< 1840px）全宽内嵌更新任务队列：紧接标题栏下方，0 横向挤压、0 遮挡卡片 */}
      {!isUltrawide && queueJobs.length > 0 && !queueCollapsed && (
        <UpdateQueuePanel mode="inline" />
      )}

      {/* 多设备筛选 */}
      {hosts.length > 1 && (
        <div>
          <Segmented
            value={hostFilter}
            onChange={(v) => setHostFilter(String(v))}
            options={[
              { label: t.home.allHosts, value: '' },
              ...hosts.map((h) => ({ label: h.name, value: h.id })),
            ]}
          />
        </div>
      )}

      {/* 情况 A：Docker Hub 100 次/6小时未登录限流专用提醒条 */}
      {rateLimitedItems.length > 0 && (
        <div
          style={{
            borderRadius: 12,
            background: 'rgba(245, 158, 11, 0.10)',
            border: '1px solid rgba(245, 158, 11, 0.38)',
            padding: '12px 16px',
            display: 'flex',
            flexWrap: 'wrap',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 12,
            fontSize: 12,
          }}
        >
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }}>
              <span
                style={{
                  padding: '2px 6px',
                  borderRadius: 5,
                  background: '#f59e0b',
                  color: '#ffffff',
                  fontWeight: 600,
                  fontSize: 11,
                }}
              >
                {t.home.rateLimitBadge}
              </span>
              <span style={{ fontWeight: 600, fontSize: 13, color: token.colorText }}>
                {t.home.rateLimitTitle}
              </span>
            </div>
            <div style={{ color: token.colorTextSecondary, marginTop: 4 }}>
              {t.home.rateLimitDesc}
            </div>
          </div>

          <button
            type="button"
            onClick={() => setHubModalOpen(true)}
            style={{
              padding: '7px 14px',
              borderRadius: 10,
              border: 'none',
              background: '#f59e0b',
              color: '#ffffff',
              fontSize: 12,
              fontWeight: 600,
              cursor: 'pointer',
              flexShrink: 0,
            }}
          >
            {t.home.rateLimitBtn}
          </button>
        </div>
      )}

      {/* 情况 B：私有仓库未授权或其他单行紧凑提示条 */}
      {otherFailedItems.length > 0 && (
        <div
          style={{
            borderRadius: 12,
            background: token.colorBgContainer,
            border: `1px solid ${token.colorBorderSecondary}`,
            padding: '10px 14px',
            fontSize: 12,
            display: 'flex',
            flexDirection: 'column',
            gap: 6,
          }}
        >
          <div
            style={{
              display: 'flex',
              flexWrap: 'wrap',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 8,
            }}
          >
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                minWidth: 0,
                flex: 1,
                flexWrap: 'wrap',
              }}
            >
              <span style={{ color: '#f59e0b', fontWeight: 600, flexShrink: 0 }}>
                ⚠{' '}
                {otherFailedItems.length === 1
                  ? t.home.failedBannerSingle
                  : t.home.failedBannerMulti.replace('{count}', String(otherFailedItems.length))}
              </span>
              <span
                style={{
                  color: token.colorText,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                }}
              >
                {formatFailedItemLine(otherFailedItems[0])}
              </span>
              {otherFailedItems.length > 1 && (
                <button
                  type="button"
                  onClick={() => setFailedExpanded((v) => !v)}
                  style={{
                    border: 'none',
                    background: 'transparent',
                    color: token.colorPrimary,
                    fontSize: 12,
                    cursor: 'pointer',
                    padding: '0 4px',
                  }}
                >
                  {failedExpanded ? t.home.failedCollapse : t.home.failedExpandAll}
                </button>
              )}
            </div>

            <button
              type="button"
              onClick={() => navigate('/settings')}
              style={{
                border: 'none',
                background: 'transparent',
                color: token.colorPrimary,
                fontWeight: 500,
                fontSize: 12,
                cursor: 'pointer',
                padding: 0,
                flexShrink: 0,
              }}
            >
              {t.home.failedGoCreds}
            </button>
          </div>

          {failedExpanded && otherFailedItems.length > 1 && (
            <ul
              style={{
                margin: '4px 0 0',
                paddingLeft: 20,
                color: token.colorTextSecondary,
                lineHeight: 1.65,
              }}
            >
              {otherFailedItems.slice(1).map((f) => (
                <li key={f.serviceKey} style={{ color: token.colorText }}>
                  {formatFailedItemLine(f)}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {/* 首次尚无应用数据且正在扫描时，不渲染虚假 0 空结论 */}
      {isInitialScanning ? (
        <div
          style={{
            borderRadius: 14,
            background: token.colorBgContainer,
            border: `1px solid ${token.colorBorderSecondary}`,
            padding: '48px 16px',
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            gap: 12,
            color: token.colorTextSecondary,
            fontSize: 13,
          }}
        >
          <Spin />
          <span>{renderHeaderSubtitle()}</span>
        </div>
      ) : (
        <>
          {/* 分组 1：可更新 */}
          {renderGroupSection(
            t.home.groupNormal,
            normalList.length,
            token.colorPrimary,
            '#ffffff',
            normalExpanded,
            () => setNormalExpanded((v) => !v),
            renderGrid(normalList, t.home.emptyNormal),
          )}

          {/* 分组 2：需谨慎更新 */}
          {(cautionList.length > 0 || !isRunning) &&
            renderGroupSection(
              t.home.groupCaution,
              cautionList.length,
              '#f59e0b',
              '#ffffff',
              cautionExpanded,
              () => setCautionToggled(!cautionExpanded),
              renderGrid(cautionList, t.home.emptyCaution),
            )}

          {/* 分组 3：无需更新（合并已是最新与已忽略/锁定） */}
          {renderGroupSection(
            t.home.groupNoUpdate,
            noUpdateList.length,
            token.colorFillSecondary,
            token.colorText,
            noUpdateExpanded,
            () => setNoUpdateExpanded((v) => !v),
            renderGrid(noUpdateList, t.home.emptyNoUpdate),
          )}
        </>
      )}

      {/* 一键登录 Docker Hub 账号弹窗 */}
      <Modal
        open={hubModalOpen}
        title={t.home.hubModalTitle}
        onCancel={() => setHubModalOpen(false)}
        okText={t.home.hubSaveAndRecheck}
        cancelText={t.ignoreModal.cancel}
        confirmLoading={hubSubmitting}
        onOk={handleSaveHubAndRecheck}
        width={440}
        destroyOnHidden
      >
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))',
            gap: 12,
            paddingTop: 8,
          }}
        >
          <div>
            <div style={{ fontSize: 12, color: token.colorTextSecondary, marginBottom: 4 }}>
              {t.home.hubUserLabel}
            </div>
            <Input
              value={hubUser}
              onChange={(e) => setHubUser(e.target.value)}
              placeholder={t.home.hubUserPlaceholder}
              autoFocus
            />
          </div>
          <div>
            <div style={{ fontSize: 12, color: token.colorTextSecondary, marginBottom: 4 }}>
              {t.home.hubSecretLabel}
            </div>
            <Input.Password
              value={hubSecret}
              onChange={(e) => setHubSecret(e.target.value)}
              placeholder={t.home.hubSecretPlaceholder}
              onPressEnter={handleSaveHubAndRecheck}
            />
          </div>
        </div>
      </Modal>
    </div>
  )
}
