import { useEffect, useMemo, useState } from 'react'
import {
  Badge,
  Button,
  Card,
  Col,
  Collapse,
  Empty,
  Flex,
  Form,
  Input,
  Modal,
  Row,
  Segmented,
  Select,
  Space,
  Spin,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import { PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, showApiError, type AppCardItem, type FailedItem } from '../api/client'
import AppCard from '../components/AppCard'
import { formatHostEndpoint, formatShortNames } from '../components/noticeUtils'
import StatusNoticeBar, { type StatusNoticeRow } from '../components/StatusNoticeBar'
import UpdateQueuePanel from '../components/UpdateQueuePanel'
import { useIsUltrawide, useOrderedQueueJobs } from '../hooks/useUpdateQueue'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

function extractRegistryDomain(item: { registry?: string; imageRef?: string }): string {
  if (item.registry) return item.registry.trim().toLowerCase()
  const raw = (item.imageRef || '').trim().split('@')[0]
  if (!raw) return ''
  const firstSlash = raw.indexOf('/')
  if (firstSlash < 0) return 'docker.io'
  const first = raw.slice(0, firstSlash).toLowerCase()
  if (first.includes('.') || first.includes(':') || first === 'localhost') {
    if (
      first === 'index.docker.io' ||
      first === 'registry-1.docker.io' ||
      first === 'registry.hub.docker.com'
    ) {
      return 'docker.io'
    }
    return first
  }
  return 'docker.io'
}

export default function HomePage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const navigate = useNavigate()

  const [hostFilter, setHostFilter] = useState<string>('')
  const [tagFilter, setTagFilter] = useState<string>('')
  const liveProgress = useUIStore((s) => s.liveProgress)
  const setLiveProgress = useUIStore((s) => s.setLiveProgress)
  const setActiveMirrorHostId = useUIStore((s) => s.setActiveMirrorHostId)
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
  const [hubForm] = Form.useForm<{ username: string; secret: string }>()
  const [hubSubmitting, setHubSubmitting] = useState(false)

  // 私有仓库凭据原位设置弹窗状态
  const [regCredModalOpen, setRegCredModalOpen] = useState(false)
  const [regCredTarget, setRegCredTarget] = useState('')
  const [regCredForm] = Form.useForm<{ registry: string; username: string; secret: string }>()
  const [regCredSubmitting, setRegCredSubmitting] = useState(false)

  const regCredsQuery = useQuery({
    queryKey: ['regCreds'],
    queryFn: api.listRegCreds,
    enabled: regCredModalOpen,
  })

  const overview = useQuery({
    queryKey: ['overview', hostFilter, locale],
    queryFn: () => api.overview(hostFilter || undefined),
  })

  const progress = liveProgress ?? overview.data?.progress
  const isRunning = progress?.running ?? false

  const rawNormalList = useMemo(() => overview.data?.normal ?? [], [overview.data?.normal])
  const rawCautionList = useMemo(() => overview.data?.caution ?? [], [overview.data?.caution])
  const rawNoUpdateList = useMemo(
    () => overview.data?.noUpdate ?? overview.data?.ignored ?? [],
    [overview.data?.ignored, overview.data?.noUpdate],
  )
  const updateCount = rawNormalList.length + rawCautionList.length

  const tagCounts = useMemo(() => {
    const appTagsByKey = new Map<string, Set<string>>()
    for (const card of [...rawNormalList, ...rawCautionList, ...rawNoUpdateList]) {
      let set = appTagsByKey.get(card.key)
      if (!set) {
        set = new Set<string>()
        appTagsByKey.set(card.key, set)
      }
      for (const tg of card.meta?.tags ?? []) {
        if (tg.trim()) set.add(tg.trim())
      }
      const svcs = card.allServices?.length ? card.allServices : card.services
      for (const s of svcs) {
        for (const tg of s.meta?.tags ?? []) {
          if (tg.trim()) set.add(tg.trim())
        }
      }
    }
    const counts = new Map<string, number>()
    for (const set of appTagsByKey.values()) {
      for (const tg of set) {
        counts.set(tg, (counts.get(tg) ?? 0) + 1)
      }
    }
    return Array.from(counts.entries()).sort((a, b) => a[0].localeCompare(b[0]))
  }, [rawCautionList, rawNoUpdateList, rawNormalList])

  const effectiveTagFilter = useMemo(
    () => (tagFilter && tagCounts.some(([tg]) => tg === tagFilter) ? tagFilter : ''),
    [tagCounts, tagFilter],
  )

  const matchesCardTag = (card: AppCardItem): boolean => {
    if (!effectiveTagFilter) return true
    if (card.meta?.tags?.includes(effectiveTagFilter)) return true
    const svcs = card.allServices?.length ? card.allServices : card.services
    return svcs.some((s) => s.meta?.tags?.includes(effectiveTagFilter))
  }

  const normalList = rawNormalList.filter(matchesCardTag)
  const cautionList = rawCautionList.filter(matchesCardTag)
  const noUpdateList = rawNoUpdateList.filter(matchesCardTag)

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
      if (Array.isArray(progress.completedServiceKeys) || !progress.currentServiceKey) {
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
    if (isRunning) return
    try {
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      showApiError(e)
    }
  }

  const handleSaveHubAndRecheck = async () => {
    const raw = hubForm.getFieldsValue()
    const username = (raw.username || '').trim()
    const secret = (raw.secret || '').trim()
    if (!username || !secret) {
      message.warning(t.home.hubRequiredMsg)
      return
    }
    setHubSubmitting(true)
    try {
      await api.saveRegCred({ registry: 'docker.io', username, secret })
      qc.invalidateQueries({ queryKey: ['regCreds'] })
      setHubModalOpen(false)
      hubForm.setFieldValue('secret', '')
      message.success(t.home.hubSavedRechecking)
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      showApiError(e)
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

  const checkFillPercent = useMemo((): number => {
    if (!isRunning) return 0
    if (progress?.phase === 'discovering') return 6
    const tot = progress?.total ?? 0
    if (tot > 0) {
      const pct = ((progress?.completed ?? 0) / tot) * 100
      return Math.min(100, Math.max(6, Number(pct.toFixed(1))))
    }
    return 6
  }, [isRunning, progress?.completed, progress?.phase, progress?.total])

  const checkTooltipTitle = useMemo(() => {
    if (!isRunning) return undefined
    let text = `${t.home.checkingProgress}…`
    if (progress?.phase === 'discovering') {
      text = progress.hostName
        ? `${t.home.discoveringHost}${progress.hostName}`
        : t.home.discoveringGeneric
    } else if ((progress?.total ?? 0) > 0) {
      const done = progress?.completed ?? 0
      const tot = progress!.total
      const pct = Math.min(100, Math.round((done / tot) * 100))
      text = t.home.checkingTooltipCount
        .replace('{completed}', String(done))
        .replace('{total}', String(tot))
        .replace('{percent}', String(pct))
    }
    return <span style={{ fontVariantNumeric: 'tabular-nums' }}>{text}</span>
  }, [
    isRunning,
    progress?.completed,
    progress?.hostName,
    progress?.phase,
    progress?.total,
    t.home.checkingProgress,
    t.home.checkingTooltipCount,
    t.home.discoveringGeneric,
    t.home.discoveringHost,
  ])

  const hosts = useMemo(() => overview.data?.hosts ?? [], [overview.data?.hosts])
  const rawFailed = overview.data?.failed

  const rateLimitedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind === 'rate_limited'),
    [rawFailed],
  )
  const otherFailedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind !== 'rate_limited'),
    [rawFailed],
  )

  const authRequiredRegistries = useMemo(() => {
    const seen = new Set<string>()
    const list: string[] = []
    for (const f of otherFailedItems) {
      if (f.errorKind !== 'auth_required') continue
      const reg = extractRegistryDomain(f)
      if (reg && !seen.has(reg)) {
        seen.add(reg)
        list.push(reg)
      }
    }
    return list
  }, [otherFailedItems])

  const openRegCredModal = (initialReg?: string) => {
    const target = initialReg || authRequiredRegistries[0] || ''
    const saved = regCredsQuery.data?.items?.find((c) => c.registry === target)
    setRegCredTarget(target)
    regCredForm.setFieldsValue({
      registry: target,
      username: saved?.username ?? '',
      secret: '',
    })
    setRegCredModalOpen(true)
  }

  useEffect(() => {
    if (!regCredModalOpen || !regCredTarget) return
    const curUser = regCredForm.getFieldValue('username')
    if (!curUser) {
      const saved = regCredsQuery.data?.items?.find((c) => c.registry === regCredTarget)
      if (saved?.username) {
        regCredForm.setFieldValue('username', saved.username)
      }
    }
  }, [regCredForm, regCredModalOpen, regCredTarget, regCredsQuery.data])

  const handleSaveRegCredAndRecheck = async () => {
    const raw = regCredForm.getFieldsValue()
    const registry = (raw.registry || regCredTarget || '').trim()
    const username = (raw.username || '').trim()
    const secret = (raw.secret || '').trim()
    if (!registry || !username || !secret) {
      message.warning(t.home.regCredRequiredMsg)
      return
    }
    setRegCredSubmitting(true)
    try {
      await api.saveRegCred({ registry, username, secret })
      qc.invalidateQueries({ queryKey: ['regCreds'] })
      setRegCredModalOpen(false)
      regCredForm.setFieldValue('secret', '')
      message.success(t.home.regCredSavedRechecking)
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      showApiError(e)
    } finally {
      setRegCredSubmitting(false)
    }
  }

  const displayFailedServiceName = (f: FailedItem): string =>
    f.serviceName && f.serviceName !== 'main' ? f.serviceName : f.appName

  const offlineHosts = useMemo(
    () =>
      hosts.filter(
        (h) => (!hostFilter || h.id === hostFilter) && h.status === 'offline',
      ),
    [hostFilter, hosts],
  )

  const networkFailedHostIds = useMemo(() => {
    const seen = new Set<string>()
    const list: string[] = []
    for (const f of otherFailedItems) {
      if (f.errorKind === 'network' && f.hostId && !seen.has(f.hostId)) {
        seen.add(f.hostId)
        list.push(f.hostId)
      }
    }
    return list
  }, [otherFailedItems])

  const offlineHostNotice = useMemo(() => {
    if (offlineHosts.length === 0) return null
    const title =
      offlineHosts.length === 1
        ? t.home.hostOfflineSingle
        : t.home.hostOfflineMulti.replace('{count}', String(offlineHosts.length))
    const names = formatShortNames(
      offlineHosts.map((h) => h.name),
      t.home.nameListSep,
      t.home.nameListMore,
    )
    const summary =
      offlineHosts.length === 1
        ? `${offlineHosts[0].name} (${formatHostEndpoint(offlineHosts[0], t.home.localHostEndpoint)}) · ${
            offlineHosts[0].lastError || t.home.hostOfflineDefaultReason
          }`
        : `${names} · ${t.home.hostOfflineMultiSummary}`
    const rows: StatusNoticeRow[] = offlineHosts.map((h) => ({
      key: h.id,
      name: h.name,
      sub: formatHostEndpoint(h, t.home.localHostEndpoint),
      reason: h.lastError || t.home.hostOfflineDefaultReason,
      actionText: t.home.hostOfflineFixRow,
      onAction: () => navigate('/hosts'),
    }))
    return { title, summary, rows }
  }, [
    navigate,
    offlineHosts,
    t.home.hostOfflineDefaultReason,
    t.home.hostOfflineFixRow,
    t.home.hostOfflineMulti,
    t.home.hostOfflineMultiSummary,
    t.home.hostOfflineSingle,
    t.home.localHostEndpoint,
    t.home.nameListMore,
    t.home.nameListSep,
  ])

  const rateLimitedNotice = useMemo(() => {
    if (rateLimitedItems.length === 0) return null
    const names = formatShortNames(
      rateLimitedItems.map(displayFailedServiceName),
      t.home.nameListSep,
      t.home.nameListMore,
    )
    const rows: StatusNoticeRow[] = rateLimitedItems.map((f) => ({
      key: f.serviceKey,
      name: displayFailedServiceName(f),
      sub: f.hostName,
      reason: t.home.rateLimitRowReason,
    }))
    return {
      title: t.home.rateLimitShortTitle,
      summary: t.home.rateLimitSummary.replace('{names}', names),
      rows,
    }
  }, [
    rateLimitedItems,
    t.home.nameListMore,
    t.home.nameListSep,
    t.home.rateLimitRowReason,
    t.home.rateLimitShortTitle,
    t.home.rateLimitSummary,
  ])

  const otherFailedNotice = useMemo(() => {
    if (otherFailedItems.length === 0) return null
    const title =
      otherFailedItems.length === 1
        ? t.home.failedTitleSingle
        : t.home.failedTitleMulti.replace('{count}', String(otherFailedItems.length))
    const names = formatShortNames(
      otherFailedItems.map(displayFailedServiceName),
      t.home.nameListSep,
      t.home.nameListMore,
    )
    const allAuth = otherFailedItems.every((f) => f.errorKind === 'auth_required')
    const allNetwork = otherFailedItems.every((f) => f.errorKind === 'network')
    const hasAuth = authRequiredRegistries.length > 0

    let reasonPart: string
    if (allAuth) {
      reasonPart =
        authRequiredRegistries.length === 1 && authRequiredRegistries[0]
          ? `${t.home.failedAuthShort} (${authRequiredRegistries[0]})`
          : t.home.failedAuthShort
    } else if (allNetwork) {
      reasonPart =
        otherFailedItems.length === 1 && otherFailedItems[0].errorMsg
          ? otherFailedItems[0].errorMsg
          : t.home.failedNetworkShort
    } else if (otherFailedItems.length === 1) {
      reasonPart = otherFailedItems[0].errorMsg || t.home.checkFailedStatus
    } else {
      reasonPart = t.home.failedMixedShort
    }

    const rows: StatusNoticeRow[] = otherFailedItems.map((f) => {
      const reg = extractRegistryDomain(f)
      if (f.errorKind === 'auth_required') {
        const needRowCredLink = !allAuth || authRequiredRegistries.length > 1
        return {
          key: f.serviceKey,
          name: displayFailedServiceName(f),
          sub: f.hostName,
          reason: reg ? `${t.home.failedAuthShort} (${reg})` : t.home.failedAuthShort,
          actionText: needRowCredLink ? t.home.errLinkCred : undefined,
          onAction: needRowCredLink ? () => openRegCredModal(reg) : undefined,
        }
      }
      if (f.errorKind === 'network' && f.hostId) {
        const needRowMirrorLink = hasAuth || networkFailedHostIds.length > 1 || !allNetwork
        return {
          key: f.serviceKey,
          name: displayFailedServiceName(f),
          sub: f.hostName,
          reason: f.errorMsg || t.home.failedNetworkShort,
          actionText: needRowMirrorLink ? t.home.errLinkMirror : undefined,
          onAction: needRowMirrorLink ? () => setActiveMirrorHostId(f.hostId!) : undefined,
        }
      }
      return {
        key: f.serviceKey,
        name: displayFailedServiceName(f),
        sub: f.hostName,
        reason: f.errorMsg || t.home.checkFailedStatus,
      }
    })

    let actionText: string | undefined
    let primaryAction = false
    let onAction: (() => void) | undefined
    if (hasAuth) {
      actionText = t.home.failedGoCreds
      primaryAction = true
      onAction = () => openRegCredModal()
    } else if (networkFailedHostIds.length > 0) {
      actionText = t.home.errLinkMirror
      primaryAction = false
      onAction = () => setActiveMirrorHostId(networkFailedHostIds[0])
    }

    return {
      title,
      summary: `${names} · ${reasonPart}`,
      rows,
      actionText,
      primaryAction,
      onAction,
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    authRequiredRegistries,
    networkFailedHostIds,
    otherFailedItems,
    setActiveMirrorHostId,
    t.home.checkFailedStatus,
    t.home.errLinkCred,
    t.home.errLinkMirror,
    t.home.failedAuthShort,
    t.home.failedGoCreds,
    t.home.failedMixedShort,
    t.home.failedNetworkShort,
    t.home.failedTitleMulti,
    t.home.failedTitleSingle,
    t.home.nameListMore,
    t.home.nameListSep,
  ])

  const renderGrid = (items: AppCardItem[], emptyText: string) => {
    if (items.length === 0) {
      return (
        <Card
          size="small"
          style={{
            borderStyle: 'dashed',
            textAlign: 'center',
            background: 'transparent',
          }}
        >
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {isRunning ? t.home.checkingGroupEmpty : emptyText}
          </Typography.Text>
        </Card>
      )
    }
    return (
      <Row gutter={[16, 16]} align="top">
        {items.map((item) => (
          <Col
            key={`${item.group}-${item.key}`}
            xs={24}
            md={12}
            lg={8}
          >
            <div style={{ width: '100%' }}>
              <AppCard
                app={item}
                isChecking={isCardChecking(item)}
                allTags={overview.data?.allTags}
                activeTag={effectiveTagFilter}
                onTagClick={(tg) => setTagFilter((prev) => (prev === tg ? '' : tg))}
                onOpenRegCred={(reg) => openRegCredModal(reg)}
                onChanged={() => qc.invalidateQueries({ queryKey: ['overview'] })}
              />
            </div>
          </Col>
        ))}
      </Row>
    )
  }

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
  const showCautionGroup = cautionList.length > 0 || !isRunning

  const activeGroupKeys: string[] = []
  if (normalExpanded) activeGroupKeys.push('normal')
  if (showCautionGroup && cautionExpanded) activeGroupKeys.push('caution')
  if (noUpdateExpanded) activeGroupKeys.push('noUpdate')

  const handleCollapseChange = (keys: string | string[]) => {
    const keySet = new Set(Array.isArray(keys) ? keys : [keys])
    setNormalExpanded(keySet.has('normal'))
    if (showCautionGroup) {
      setCautionToggled(keySet.has('caution'))
    }
    setNoUpdateExpanded(keySet.has('noUpdate'))
  }

  const collapseItems = [
    {
      key: 'normal',
      label: (
        <Space size={8} align="center">
          <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
            {t.home.groupNormal}
          </Typography.Text>
          <Badge count={normalList.length} showZero color={token.colorPrimary} />
        </Space>
      ),
      children: renderGrid(normalList, t.home.emptyNormal),
    },
    ...(showCautionGroup
      ? [
          {
            key: 'caution',
            label: (
              <Space size={8} align="center">
                <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
                  {t.home.groupCaution}
                </Typography.Text>
                <Badge count={cautionList.length} showZero color={token.colorWarning} />
              </Space>
            ),
            children: renderGrid(cautionList, t.home.emptyCaution),
          },
        ]
      : []),
    {
      key: 'noUpdate',
      label: (
        <Space size={8} align="center">
          <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
            {t.home.groupNoUpdate}
          </Typography.Text>
          <Badge
            count={noUpdateList.length}
            showZero
            style={{
              backgroundColor: token.colorFillSecondary,
              color: token.colorText,
              boxShadow: 'none',
            }}
          />
        </Space>
      ),
      children: renderGrid(noUpdateList, t.home.emptyNoUpdate),
    },
  ]

  return (
    <Flex vertical gap={16} style={{ width: '100%' }}>
      {/* 顶栏标题与检查按钮 */}
      <Flex justify="space-between" align="center" wrap gap={12}>
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            {t.home.title}
          </Typography.Title>
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {renderHeaderSubtitle()}
          </Typography.Text>
        </div>

        {isRunning ? (
          <Tooltip title={checkTooltipTitle} placement="topRight">
            <button
              type="button"
              aria-busy="true"
              style={{
                position: 'relative',
                overflow: 'hidden',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: 8,
                height: token.controlHeight,
                paddingInline: 15,
                borderRadius: token.borderRadius,
                fontSize: token.fontSize,
                fontFamily: 'inherit',
                fontWeight: 500,
                lineHeight: 1,
                border: `1px solid ${token.colorPrimaryBorderHover}`,
                background: 'rgba(22, 119, 255, 0.08)',
                color: token.colorPrimary,
                cursor: 'default',
                userSelect: 'none',
                transition: 'border-color 0.2s ease, background 0.2s ease',
              }}
            >
              <span
                aria-hidden="true"
                style={{
                  position: 'absolute',
                  insetBlock: 0,
                  left: 0,
                  width: `${checkFillPercent}%`,
                  background: 'rgba(22, 119, 255, 0.22)',
                  transition: 'width 0.22s ease',
                  pointerEvents: 'none',
                }}
              />
              <SyncOutlined spin style={{ position: 'relative', zIndex: 1 }} />
              <span style={{ position: 'relative', zIndex: 1 }}>{t.home.checking}</span>
            </button>
          </Tooltip>
        ) : (
          <Button type="primary" icon={<SyncOutlined />} onClick={handleCheck}>
            {t.home.checkNow}
          </Button>
        )}
      </Flex>

      {/* 中小屏（< 1980px）全宽内嵌更新任务队列：紧接标题栏下方，0 横向挤压、0 遮挡卡片 */}
      {!isUltrawide && queueJobs.length > 0 && !queueCollapsed && (
        <UpdateQueuePanel mode="inline" />
      )}

      {/* 多设备筛选与标签筛选（无标签且单设备时自动不渲染） */}
      {(hosts.length > 1 || tagCounts.length > 0) && (
        <Space wrap size={12}>
          {hosts.length > 1 && (
            <Segmented
              value={hostFilter}
              onChange={(v) => setHostFilter(String(v))}
              options={[
                { label: t.home.allHosts, value: '' },
                ...hosts.map((h) => ({ label: h.name, value: h.id })),
              ]}
            />
          )}
          {tagCounts.length > 0 && (
            <Segmented
              value={effectiveTagFilter}
              onChange={(v) => setTagFilter(String(v))}
              options={[
                { label: t.home.allTags, value: '' },
                ...tagCounts.map(([tg, cnt]) => ({
                  label: `${tg} (${cnt})`,
                  value: tg,
                })),
              ]}
            />
          )}
        </Space>
      )}

      {/* 统一极简白底通知条：设备连接失败 / Docker Hub 匿名限流 / 服务无法检查 */}
      {(offlineHostNotice || rateLimitedNotice || otherFailedNotice) && (
        <Flex vertical gap={8} style={{ width: '100%' }}>
          {offlineHostNotice && (
            <StatusNoticeBar
              level="error"
              title={offlineHostNotice.title}
              summary={offlineHostNotice.summary}
              expandText={t.home.noticeExpand}
              collapseText={t.home.noticeCollapse}
              actionText={t.home.hostOfflineGoHosts}
              onAction={() => navigate('/hosts')}
              rows={offlineHostNotice.rows}
            />
          )}

          {rateLimitedNotice && (
            <StatusNoticeBar
              level="warning"
              title={rateLimitedNotice.title}
              summary={rateLimitedNotice.summary}
              expandText={t.home.noticeExpand}
              collapseText={t.home.noticeCollapse}
              actionText={t.home.rateLimitBtn}
              primaryAction
              onAction={() => setHubModalOpen(true)}
              rows={rateLimitedNotice.rows}
            />
          )}

          {otherFailedNotice && (
            <StatusNoticeBar
              level="warning"
              title={otherFailedNotice.title}
              summary={otherFailedNotice.summary}
              expandText={t.home.noticeExpand}
              collapseText={t.home.noticeCollapse}
              actionText={otherFailedNotice.actionText}
              primaryAction={otherFailedNotice.primaryAction}
              onAction={otherFailedNotice.onAction}
              rows={otherFailedNotice.rows}
            />
          )}
        </Flex>
      )}

      {/* 首次尚无应用数据且正在扫描时，不渲染虚假 0 空结论 */}
      {isInitialScanning ? (
        <Card style={{ textAlign: 'center', padding: '36px 16px' }}>
          <Space direction="vertical" size="middle" align="center">
            <Spin />
            <Typography.Text type="secondary">{renderHeaderSubtitle()}</Typography.Text>
          </Space>
        </Card>
      ) : (
        <Collapse
          ghost
          activeKey={activeGroupKeys}
          onChange={handleCollapseChange}
          style={{ width: '100%' }}
          styles={{
            header: { paddingInline: 0, alignItems: 'center' },
            body: { paddingInline: 0, paddingTop: 4, paddingBottom: 16 },
          }}
          items={collapseItems}
        />
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
        <Form form={hubForm} layout="vertical" style={{ paddingTop: 8 }}>
          <Form.Item name="username" label={t.home.hubUserLabel} required>
            <Input placeholder={t.home.hubUserPlaceholder} autoFocus />
          </Form.Item>
          <Form.Item
            name="secret"
            label={t.home.hubSecretLabel}
            required
            style={{ marginBottom: 0 }}
          >
            <Input.Password
              placeholder={t.home.hubSecretPlaceholder}
              onPressEnter={handleSaveHubAndRecheck}
            />
          </Form.Item>
        </Form>
      </Modal>

      {/* 私有仓库凭据原位设置弹窗 */}
      <Modal
        open={regCredModalOpen}
        title={t.home.regCredModalTitle}
        onCancel={() => setRegCredModalOpen(false)}
        okText={t.home.hubSaveAndRecheck}
        cancelText={t.ignoreModal.cancel}
        confirmLoading={regCredSubmitting}
        onOk={handleSaveRegCredAndRecheck}
        width={420}
        destroyOnHidden
      >
        <Form
          form={regCredForm}
          layout="vertical"
          initialValues={{ registry: regCredTarget }}
          style={{ paddingTop: 8 }}
        >
          <Form.Item name="registry" label={t.home.regCredDomainLabel}>
            {authRequiredRegistries.length > 1 ? (
              <Select
                options={authRequiredRegistries.map((r) => ({ label: r, value: r }))}
                onChange={(val) => {
                  setRegCredTarget(val)
                  const saved = regCredsQuery.data?.items?.find((c) => c.registry === val)
                  regCredForm.setFieldsValue({
                    registry: val,
                    username: saved?.username ?? '',
                    secret: '',
                  })
                }}
                style={{ fontFamily: token.fontFamilyCode }}
              />
            ) : (
              <Input
                readOnly
                tabIndex={-1}
                style={{
                  fontFamily: token.fontFamilyCode,
                  backgroundColor: token.colorFillAlter,
                  cursor: 'default',
                }}
              />
            )}
          </Form.Item>
          <Form.Item name="username" label={t.home.regCredUserLabel} required>
            <Input placeholder={t.home.regCredUserPlaceholder} autoFocus />
          </Form.Item>
          <Form.Item
            name="secret"
            label={t.home.regCredSecretLabel}
            required
            style={{ marginBottom: 0 }}
          >
            <Input.Password
              placeholder={t.home.regCredSecretPlaceholder}
              onPressEnter={handleSaveRegCredAndRecheck}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Flex>
  )
}
