import { useEffect, useMemo, useState } from 'react'
import {
  Badge,
  Button,
  Card,
  Col,
  Empty,
  Flex,
  Input,
  Row,
  Segmented,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import {
  DeleteOutlined,
  DownOutlined,
  EditOutlined,
  ExpandAltOutlined,
  ExportOutlined,
  HddOutlined,
  PlusOutlined,
  ReloadOutlined,
  RightOutlined,
  SearchOutlined,
  ShrinkOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, type AppRuntimeTree, type ContainerRuntimeStat } from '../api/client'
import AppIconAvatar from '../components/AppIconAvatar'
import ContainerMetaModal from '../components/ContainerMetaModal'
import DeleteAppModal from '../components/DeleteAppModal'
import {
  buildMetaContextFromTree,
  pickUserTagColor,
  resolveContainerUrl,
  resolveTreeAccessUrl,
  type MetaModalContext,
} from '../components/metaUtils'
import { useI18n } from '../i18n'

function formatMem(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 MB'
  const mb = bytes / (1024 * 1024)
  if (mb >= 1024) {
    return `${(mb / 1024).toFixed(2)} GB`
  }
  return `${mb >= 10 ? mb.toFixed(0) : mb.toFixed(1)} MB`
}

function mergeFastWithCachedStats(
  fastItems: AppRuntimeTree[],
  statsItems?: AppRuntimeTree[],
): AppRuntimeTree[] {
  if (!statsItems || statsItems.length === 0) return fastItems
  const appStatsByKey = new Map<string, AppRuntimeTree>()
  for (const sApp of statsItems) {
    if (sApp.statsReady !== false) {
      appStatsByKey.set(sApp.key, sApp)
    }
  }
  if (appStatsByKey.size === 0) return fastItems

  return fastItems.map((fApp) => {
    const prevApp = appStatsByKey.get(fApp.key)
    if (!prevApp) return fApp
    const cMap = new Map<string, ContainerRuntimeStat>()
    for (const sc of prevApp.containers) {
      cMap.set(sc.id || sc.name, sc)
    }
    let cpuSum = 0
    let memSum = 0
    const mergedContainers = fApp.containers.map((fc) => {
      const prevC = cMap.get(fc.id || fc.name)
      if (!prevC || fc.state !== 'running') return fc
      cpuSum += prevC.cpuPercent
      memSum += prevC.memUsage
      return {
        ...fc,
        cpuPercent: prevC.cpuPercent,
        memUsage: prevC.memUsage,
        memLimit: prevC.memLimit,
      }
    })
    return {
      ...fApp,
      cpuPercent: Math.round(cpuSum * 100) / 100,
      memUsage: memSum,
      statsReady: true,
      containers: mergedContainers,
    }
  })
}

export default function ContainersPage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const navigate = useNavigate()

  const [hostFilter, setHostFilter] = useState<string>('')
  const [stateFilter, setStateFilter] = useState<'all' | 'running' | 'stopped'>('all')
  const [tagFilter, setTagFilter] = useState<string>('')
  const [keyword, setKeyword] = useState('')
  const [collapsedKeys, setCollapsedKeys] = useState<Record<string, boolean>>({})
  const [deletingApp, setDeletingApp] = useState<AppRuntimeTree | null>(null)
  const [editingTarget, setEditingTarget] = useState<{
    appKey: string
    containerMetaKey?: string
  } | null>(null)
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 30000)
    return () => clearInterval(id)
  }, [])

  const hostsQuery = useQuery({
    queryKey: ['hosts', locale],
    queryFn: api.listHosts,
  })

  // 本地已收录快照（纯 SQLite 读取，零远程连接等待，首次进入毫秒级直出）
  const cachedQuery = useQuery({
    queryKey: ['containers-cached', hostFilter],
    queryFn: () => api.listContainers(hostFilter || undefined, { cached: true }),
    staleTime: 30000,
  })

  // 实时快速列表（跳过慢速 stats 采样，更新最新启停状态）
  const fastQuery = useQuery({
    queryKey: ['containers-fast', hostFilter],
    queryFn: () => api.listContainers(hostFilter || undefined, { withStats: false }),
    staleTime: 10000,
  })

  // 完整实时资源占用轮询（异步补齐 CPU / 内存并在后台定时刷新，同时写回本地快照）
  const statsQuery = useQuery({
    queryKey: ['containers', hostFilter],
    queryFn: () => api.listContainers(hostFilter || undefined, { withStats: true }),
    refetchInterval: 10000,
  })

  const hosts = useMemo(() => hostsQuery.data?.items ?? [], [hostsQuery.data?.items])

  const { rawTrees, isShowingCached } = useMemo(() => {
    const statsItems = statsQuery.data?.items
    const fastItems = fastQuery.data?.items
    const cachedItems = cachedQuery.data?.items
    if (statsItems && (!fastItems || statsQuery.dataUpdatedAt >= fastQuery.dataUpdatedAt)) {
      return { rawTrees: statsItems, isShowingCached: false }
    }
    if (fastItems) {
      return {
        rawTrees: mergeFastWithCachedStats(fastItems, statsItems ?? cachedItems),
        isShowingCached: false,
      }
    }
    if (cachedItems && cachedItems.length > 0) {
      return { rawTrees: cachedItems, isShowingCached: true }
    }
    return { rawTrees: statsItems ?? cachedItems ?? [], isShowingCached: false }
  }, [
    cachedQuery.data?.items,
    fastQuery.data?.items,
    fastQuery.dataUpdatedAt,
    statsQuery.data?.items,
    statsQuery.dataUpdatedAt,
  ])

  const distinctHostCount = useMemo(() => {
    const ids = new Set<string>()
    for (const h of hosts) ids.add(h.id)
    for (const a of rawTrees) {
      if (a.hostId) ids.add(a.hostId)
    }
    return ids.size
  }, [hosts, rawTrees])

  // 当存在多台设备且处于「全部设备」视图时，独立展示「所属设备」列；单设备或已筛选具体设备时自动收起冗余列
  const showHostCol = distinctHostCount > 1 && !hostFilter

  const colSpan = showHostCol
    ? {
        name: { xs: 10, md: 7 },
        host: { xs: 4, md: 3 },
        state: { xs: 4, md: 3 },
        uptime: { xs: 0, md: 3 },
        resource: { xs: 4, md: 3 },
        image: { xs: 0, md: 3 },
        action: { xs: 2, md: 2 },
      }
    : {
        name: { xs: 11, md: 8 },
        host: { xs: 0, md: 0 },
        state: { xs: 5, md: 3 },
        uptime: { xs: 0, md: 3 },
        resource: { xs: 5, md: 3 },
        image: { xs: 0, md: 5 },
        action: { xs: 3, md: 2 },
      }

  const allTagsPool = useMemo(
    () =>
      statsQuery.data?.allTags ??
      fastQuery.data?.allTags ??
      cachedQuery.data?.allTags ??
      [],
    [cachedQuery.data?.allTags, fastQuery.data?.allTags, statsQuery.data?.allTags],
  )

  const tagCounts = useMemo(() => {
    const counts = new Map<string, number>()
    for (const app of rawTrees) {
      const set = new Set<string>()
      for (const tg of app.meta?.tags ?? []) {
        if (tg.trim()) set.add(tg.trim())
      }
      for (const c of app.containers) {
        for (const tg of c.meta?.tags ?? []) {
          if (tg.trim()) set.add(tg.trim())
        }
      }
      for (const tg of set) {
        counts.set(tg, (counts.get(tg) ?? 0) + 1)
      }
    }
    return Array.from(counts.entries()).sort((a, b) => a[0].localeCompare(b[0]))
  }, [rawTrees])

  const effectiveTagFilter = useMemo(
    () => (tagFilter && tagCounts.some(([tg]) => tg === tagFilter) ? tagFilter : ''),
    [tagCounts, tagFilter],
  )

  const counts = useMemo(() => {
    let all = 0
    let running = 0
    let stopped = 0
    for (const app of rawTrees) {
      for (const c of app.containers) {
        all++
        if (c.state === 'running') running++
        else stopped++
      }
    }
    return { all, running, stopped }
  }, [rawTrees])

  const filteredTrees = useMemo(() => {
    const q = keyword.trim().toLowerCase()
    const result: AppRuntimeTree[] = []
    for (const app of rawTrees) {
      const appTags = app.meta?.tags ?? []
      if (effectiveTagFilter) {
        const appHasTag = appTags.includes(effectiveTagFilter)
        const anyChildHasTag = app.containers.some((c) =>
          c.meta?.tags?.includes(effectiveTagFilter),
        )
        if (!appHasTag && !anyChildHasTag) continue
      }

      const appAlias = (app.meta?.alias || '').toLowerCase()
      const appOrHostMatches = q
        ? app.name.toLowerCase().includes(q) ||
          appAlias.includes(q) ||
          appTags.some((tg) => tg.toLowerCase().includes(q)) ||
          app.hostName.toLowerCase().includes(q)
        : true

      const appHasActiveTag = effectiveTagFilter ? appTags.includes(effectiveTagFilter) : true

      const matchedContainers = app.containers.filter((c) => {
        const isRunning = c.state === 'running'
        if (stateFilter === 'running' && !isRunning) return false
        if (stateFilter === 'stopped' && isRunning) return false
        if (effectiveTagFilter && !appHasActiveTag && !c.meta?.tags?.includes(effectiveTagFilter)) {
          return false
        }
        if (!q) return true
        if (appOrHostMatches) return true
        const cAlias = (c.meta?.alias || '').toLowerCase()
        const cTags = c.meta?.tags ?? []
        return (
          c.name.toLowerCase().includes(q) ||
          cAlias.includes(q) ||
          cTags.some((tg) => tg.toLowerCase().includes(q)) ||
          (c.service && c.service.toLowerCase().includes(q)) ||
          c.image.toLowerCase().includes(q)
        )
      })
      if (matchedContainers.length > 0) {
        result.push({
          ...app,
          containers: matchedContainers,
        })
      }
    }
    return result
  }, [rawTrees, stateFilter, effectiveTagFilter, keyword])

  const multiContainerKeys = useMemo(
    () => filteredTrees.filter((a) => a.containers.length > 1).map((a) => a.key),
    [filteredTrees],
  )

  const allCollapsed =
    multiContainerKeys.length > 0 && multiContainerKeys.every((k) => Boolean(collapsedKeys[k]))

  const toggleAll = () => {
    if (allCollapsed) {
      setCollapsedKeys({})
    } else {
      const next: Record<string, boolean> = {}
      for (const k of multiContainerKeys) {
        next[k] = true
      }
      setCollapsedKeys(next)
    }
  }

  const toggleOne = (key: string) => {
    setCollapsedKeys((prev) => ({ ...prev, [key]: !prev[key] }))
  }

  const handleRefresh = () => {
    void cachedQuery.refetch()
    void fastQuery.refetch()
    void statsQuery.refetch()
  }

  const isRefreshing = fastQuery.isFetching || statsQuery.isFetching
  const isInitialLoading =
    rawTrees.length === 0 && fastQuery.isPending && statsQuery.isPending

  const formatUptime = (c: ContainerRuntimeStat): string => {
    if (c.state !== 'running') {
      return t.containers.stateStopped
    }
    if (c.uptime) {
      const cleaned = c.uptime.replace(/\s*\([^)]*\)\s*$/, '').trim()
      if (locale === 'zh-CN' && /^up\s+/i.test(cleaned)) {
        const body = cleaned.replace(/^up\s+/i, '').trim()
        if (/^about an hour/i.test(body)) return '已运行 1 小时'
        if (/^about a minute/i.test(body) || /^less than a second/i.test(body)) return t.containers.uptimeJustNow
        const m = body.match(/^(\d+)\s+(second|minute|hour|day|week|month|year)s?/i)
        if (m) {
          const n = Number(m[1])
          const unit = m[2].toLowerCase()
          if (unit === 'day') return `已运行 ${n} 天`
          if (unit === 'week') return `已运行 ${n * 7} 天`
          if (unit === 'month') return `已运行 ${n * 30} 天`
          if (unit === 'year') return `已运行 ${n * 365} 天`
          if (unit === 'hour') return `已运行 ${n} 小时`
          if (unit === 'minute') return `已运行 ${n} 分钟`
          return t.containers.uptimeJustNow
        }
      }
      if (cleaned) return cleaned
    }
    if (c.startedAt && c.startedAt > 0) {
      const diff = Math.max(0, nowSec - c.startedAt)
      const days = Math.floor(diff / 86400)
      const hours = Math.floor((diff % 86400) / 3600)
      const mins = Math.floor((diff % 3600) / 60)
      if (days > 0) {
        return t.containers.uptimeDaysHours.replace('{d}', String(days)).replace('{h}', String(hours))
      }
      if (hours > 0) {
        return t.containers.uptimeHoursMins.replace('{h}', String(hours)).replace('{m}', String(mins))
      }
      if (mins > 0) {
        return t.containers.uptimeMins.replace('{m}', String(mins))
      }
      return t.containers.uptimeJustNow
    }
    return t.containers.stateRunning
  }

  const formatProjectUptime = (app: AppRuntimeTree): string => {
    const runningList = app.containers.filter((c) => c.state === 'running')
    if (runningList.length === 0) return t.containers.stateStopped
    const withStarted = runningList.filter((c) => c.startedAt && c.startedAt > 0)
    if (withStarted.length > 0) {
      const oldest = withStarted.reduce((acc, cur) =>
        (cur.startedAt ?? 0) < (acc.startedAt ?? 0) ? cur : acc,
      )
      return formatUptime(oldest)
    }
    return formatUptime(runningList[0])
  }

  const renderStateCell = (c: ContainerRuntimeStat) => {
    if (c.state === 'running') {
      return (
        <Space size={6} wrap>
          <Badge status="success" text={t.containers.stateRunning} />
          {c.health === 'healthy' && (
            <Tag color="success" style={{ margin: 0 }}>
              {t.containers.healthHealthy}
            </Tag>
          )}
          {c.health === 'unhealthy' && (
            <Tag color="error" style={{ margin: 0 }}>
              {t.containers.healthUnhealthy}
            </Tag>
          )}
          {c.health === 'starting' && (
            <Tag color="processing" style={{ margin: 0 }}>
              {t.containers.healthStarting}
            </Tag>
          )}
        </Space>
      )
    }
    if (c.state === 'restarting') {
      return <Badge status="warning" text={t.containers.stateRestarting} />
    }
    if (c.state === 'paused') {
      return <Badge status="warning" text={t.containers.statePaused} />
    }
    return <Badge status="default" text={t.containers.stateStopped} />
  }

  const renderHostCell = (hostName: string, muted = false) => (
    <Flex align="center" gap={6} style={{ minWidth: 0, paddingRight: 8, opacity: muted ? 0.65 : 1 }}>
      <HddOutlined style={{ color: token.colorTextTertiary, fontSize: token.fontSizeSM, flexShrink: 0 }} />
      <Typography.Text
        type="secondary"
        style={{ fontSize: token.fontSizeSM }}
        ellipsis={{ tooltip: hostName }}
      >
        {hostName}
      </Typography.Text>
    </Flex>
  )

  const renderImageCell = (image: string) => (
    <Tooltip title={image}>
      <Typography.Text
        type="secondary"
        style={{
          fontSize: token.fontSizeSM,
          fontFamily: token.fontFamilyCode,
          maxWidth: '100%',
          display: 'inline-block',
        }}
        ellipsis
      >
        {image}
      </Typography.Text>
    </Tooltip>
  )

  const editingMetaContext: MetaModalContext | null = useMemo(() => {
    if (!editingTarget) return null
    const tree = rawTrees.find((a) => a.key === editingTarget.appKey)
    if (!tree) return null
    const targetC = editingTarget.containerMetaKey
      ? tree.containers.find(
          (c) =>
            (c.metaKey || `${tree.key}/ctr/${c.name}`) === editingTarget.containerMetaKey,
        )
      : undefined
    return buildMetaContextFromTree(tree, targetC, t.metaModal.wholeProject)
  }, [editingTarget, rawTrees, t.metaModal.wholeProject])

  const renderResourceCell = (
    isRunning: boolean,
    statsReady: boolean,
    cpuPercent: number,
    memUsage: number,
    strong = false,
  ) => {
    if (!isRunning) {
      return <Typography.Text style={{ fontSize: token.fontSizeSM }}>-</Typography.Text>
    }
    if (!statsReady) {
      return (
        <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
          {t.containers.statsLoading}
        </Typography.Text>
      )
    }
    return (
      <div style={{ lineHeight: 1.4 }}>
        <div>
          <Typography.Text
            type="secondary"
            style={{ fontSize: token.fontSizeSM, marginRight: 6 }}
          >
            {t.containers.cpuLabel}
          </Typography.Text>
          <Typography.Text
            strong={strong}
            style={{ fontSize: token.fontSizeSM, fontFamily: token.fontFamilyCode }}
          >
            {`${cpuPercent.toFixed(1)}%`}
          </Typography.Text>
        </div>
        <div>
          <Typography.Text
            type="secondary"
            style={{ fontSize: token.fontSizeSM, marginRight: 6 }}
          >
            {t.containers.memLabel}
          </Typography.Text>
          <Typography.Text
            type="secondary"
            style={{ fontSize: token.fontSizeSM, fontFamily: token.fontFamilyCode }}
          >
            {formatMem(memUsage)}
          </Typography.Text>
        </div>
      </div>
    )
  }

  const renderEditIconBtn = (onClick: (e: React.MouseEvent) => void) => (
    <Tooltip title={t.containers.editInfoTip}>
      <Button
        type="text"
        size="small"
        aria-label={t.containers.editInfoTip}
        icon={<EditOutlined style={{ fontSize: 13 }} />}
        onClick={onClick}
        style={{
          width: 22,
          height: 22,
          minWidth: 22,
          padding: 0,
          color: token.colorTextTertiary,
          flexShrink: 0,
        }}
      />
    </Tooltip>
  )

  const renderUserTags = (tags?: string[]) => {
    if (!tags || tags.length === 0) return null
    return tags.map((tg) => {
      const active = effectiveTagFilter === tg
      return (
        <Tag
          key={tg}
          variant={active ? 'solid' : 'filled'}
          color={active ? 'blue' : pickUserTagColor(tg)}
          onClick={(e) => {
            e.stopPropagation()
            setTagFilter((prev) => (prev === tg ? '' : tg))
          }}
          style={{ margin: 0, cursor: 'pointer', userSelect: 'none', flexShrink: 0 }}
        >
          {tg}
        </Tag>
      )
    })
  }

  const renderOpenIconBtn = (url: string | null) => {
    if (!url) return null
    const tip = t.containers.openWebTip.replace('{url}', url)
    return (
      <Tooltip title={tip}>
        <Button
          type="text"
          size="small"
          aria-label={tip}
          icon={<ExportOutlined />}
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          onClick={(e) => e.stopPropagation()}
          style={{ color: token.colorTextSecondary }}
        />
      </Tooltip>
    )
  }

  if (!hostsQuery.isPending && hosts.length === 0) {
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

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center" wrap gap={12}>
        <Space size={10} align="center" wrap>
          <Typography.Title level={3} style={{ margin: 0 }}>
            {t.containers.title}
          </Typography.Title>
          {isShowingCached && isRefreshing && (
            <Tag icon={<SyncOutlined spin />} color="processing" style={{ margin: 0 }}>
              {t.containers.syncingLiveBadge}
            </Tag>
          )}
        </Space>
        <Space wrap>
          {multiContainerKeys.length > 0 && (
            <Button
              icon={allCollapsed ? <ExpandAltOutlined /> : <ShrinkOutlined />}
              onClick={toggleAll}
            >
              {allCollapsed ? t.containers.expandAll : t.containers.collapseAll}
            </Button>
          )}
          <Button
            icon={<ReloadOutlined spin={isRefreshing} />}
            loading={isRefreshing && isInitialLoading}
            onClick={handleRefresh}
          >
            {t.containers.refresh}
          </Button>
        </Space>
      </Flex>

      {/* 筛选工具栏 */}
      <Flex justify="space-between" align="center" wrap gap={12}>
        <Space wrap size={12}>
          {hosts.length > 1 && (
            <Segmented
              value={hostFilter}
              onChange={(v) => setHostFilter(String(v))}
              options={[
                { label: t.containers.allHosts, value: '' },
                ...hosts.map((h) => ({ label: h.name, value: h.id })),
              ]}
            />
          )}
          <Segmented
            value={stateFilter}
            onChange={(v) => setStateFilter(v as 'all' | 'running' | 'stopped')}
            options={[
              {
                label: t.containers.filterAll.replace('{count}', String(counts.all)),
                value: 'all',
              },
              {
                label: t.containers.filterRunning.replace('{count}', String(counts.running)),
                value: 'running',
              },
              {
                label: t.containers.filterStopped.replace('{count}', String(counts.stopped)),
                value: 'stopped',
              },
            ]}
          />
          {tagCounts.length > 0 && (
            <Segmented
              value={effectiveTagFilter}
              onChange={(v) => setTagFilter(String(v))}
              options={[
                { label: t.containers.allTags, value: '' },
                ...tagCounts.map(([tg, cnt]) => ({
                  label: `${tg} (${cnt})`,
                  value: tg,
                })),
              ]}
            />
          )}
        </Space>
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder={t.containers.searchPlaceholder}
          value={keyword}
          onChange={(e) => setKeyword(e.target.value)}
          style={{ width: 280, maxWidth: '100%' }}
        />
      </Flex>

      {isInitialLoading ? (
        <Flex justify="center" style={{ padding: 48 }}>
          <Spin />
        </Flex>
      ) : filteredTrees.length === 0 ? (
        <Card>
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t.containers.empty} />
        </Card>
      ) : (
        <Card styles={{ body: { padding: 0 } }} style={{ overflow: 'hidden' }}>
          {/* 统一表头（设备与当前镜像完全拆分独立） */}
          <Row
            align="middle"
            style={{
              padding: '12px 16px',
              background: token.colorFillAlter,
              borderBottom: `1px solid ${token.colorBorderSecondary}`,
              fontSize: token.fontSizeSM,
              fontWeight: 600,
              color: token.colorTextSecondary,
            }}
          >
            <Col {...colSpan.name}>{t.containers.colName}</Col>
            {showHostCol && <Col {...colSpan.host}>{t.containers.colHost}</Col>}
            <Col {...colSpan.state}>{t.containers.colState}</Col>
            <Col {...colSpan.uptime}>{t.containers.colUptime}</Col>
            <Col {...colSpan.resource}>{t.containers.colResource}</Col>
            <Col {...colSpan.image}>{t.containers.colImage}</Col>
            <Col {...colSpan.action} style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
              {t.containers.colActions}
            </Col>
          </Row>

          {/* 树状双行列表 */}
          {filteredTrees.map((app, appIdx) => {
            const isProject = app.containers.length > 1
            const collapsed = Boolean(collapsedKeys[app.key])
            const isLastApp = appIdx === filteredTrees.length - 1
            const statsReady = app.statsReady !== false
            const appAlias = (app.meta?.alias || '').trim()
            const appTitle = appAlias || app.name
            const hasAppAlias = Boolean(appAlias && appAlias !== app.name)
            const appUrl = resolveTreeAccessUrl(app)

            // 单容器项目行（标准双行：左侧 38px 真实图标撑满两行 + 双行名称标签 + 双行 CPU/内存）
            if (!isProject && app.containers.length === 1) {
              const c = app.containers[0]
              const isRunning = c.state === 'running'
              const canDelete = app.source !== 'vendor_managed'
              return (
                <Row
                  key={app.key}
                  align="middle"
                  style={{
                    padding: '12px 16px',
                    borderBottom: isLastApp
                      ? 'none'
                      : `1px solid ${token.colorBorderSecondary}`,
                    fontSize: token.fontSize,
                  }}
                >
                  <Col {...colSpan.name}>
                    <Flex
                      align="center"
                      gap={12}
                      style={{ paddingLeft: 22, paddingRight: 8, minWidth: 0 }}
                    >
                      <AppIconAvatar
                        name={appTitle}
                        fallbackColorSeed={app.name}
                        iconUrl={app.iconUrl || c.iconUrl}
                        size={38}
                      />
                      <div style={{ minWidth: 0, flex: 1 }}>
                        <Flex align="center" gap={4} style={{ minWidth: 0 }}>
                          <Typography.Text
                            strong
                            ellipsis={{ tooltip: appTitle }}
                            style={{ fontSize: token.fontSize }}
                          >
                            {appTitle}
                          </Typography.Text>
                          {renderEditIconBtn(() => setEditingTarget({ appKey: app.key }))}
                        </Flex>
                        <Flex wrap="wrap" align="center" gap={6} style={{ marginTop: 3 }}>
                          {hasAppAlias && (
                            <Typography.Text
                              type="secondary"
                              style={{
                                fontSize: token.fontSizeSM,
                                fontFamily: token.fontFamilyCode,
                              }}
                            >
                              {app.name}
                            </Typography.Text>
                          )}
                          <Tag
                            variant="filled"
                            color={
                              app.source === 'dsm_project'
                                ? 'geekblue'
                                : app.source === 'compose'
                                  ? 'blue'
                                  : app.source === 'vendor_managed'
                                    ? 'purple'
                                    : undefined
                            }
                            style={{ margin: 0, flexShrink: 0 }}
                          >
                            {app.source === 'dsm_project'
                              ? t.home.tagDSM
                              : app.source === 'compose'
                                ? t.home.tagCompose
                                : app.source === 'vendor_managed'
                                  ? t.home.tagVendor
                                  : t.home.tagStandalone}
                          </Tag>
                          {renderUserTags(app.meta?.tags ?? c.meta?.tags)}
                        </Flex>
                      </div>
                    </Flex>
                  </Col>
                  {showHostCol && <Col {...colSpan.host}>{renderHostCell(app.hostName)}</Col>}
                  <Col {...colSpan.state}>{renderStateCell(c)}</Col>
                  <Col {...colSpan.uptime}>
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {formatUptime(c)}
                    </Typography.Text>
                  </Col>
                  <Col {...colSpan.resource}>
                    {renderResourceCell(isRunning, statsReady, c.cpuPercent, c.memUsage)}
                  </Col>
                  <Col {...colSpan.image}>{renderImageCell(c.image)}</Col>
                  <Col {...colSpan.action} style={{ textAlign: 'right' }}>
                    {!appUrl && !canDelete ? (
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        -
                      </Typography.Text>
                    ) : (
                      <Space size={2}>
                        {renderOpenIconBtn(appUrl)}
                        {canDelete && (
                          <Tooltip
                            title={
                              app.source === 'compose' || app.source === 'dsm_project'
                                ? t.containers.deleteProjectBtn
                                : t.containers.deleteContainerBtn
                            }
                          >
                            <Button
                              type="text"
                              danger
                              size="small"
                              aria-label={
                                app.source === 'compose' || app.source === 'dsm_project'
                                  ? t.containers.deleteProjectBtn
                                  : t.containers.deleteContainerBtn
                              }
                              icon={<DeleteOutlined />}
                              onClick={() => setDeletingApp(app)}
                            />
                          </Tooltip>
                        )}
                      </Space>
                    )}
                  </Col>
                </Row>
              )
            }

            // 多容器项目父节点 + 子容器行
            const allRunning = app.runningCount === app.totalCount && app.totalCount > 0
            const someRunning = app.runningCount > 0 && app.runningCount < app.totalCount
            const uniqueImages = Array.from(
              new Set(app.containers.map((c) => c.image).filter(Boolean)),
            )
            const canDeleteProject = app.source !== 'vendor_managed'

            return (
              <div
                key={app.key}
                style={{
                  borderBottom: isLastApp
                    ? 'none'
                    : `1px solid ${token.colorBorderSecondary}`,
                }}
              >
                {/* 父节点行 */}
                <Row
                  align="middle"
                  onClick={() => toggleOne(app.key)}
                  style={{
                    padding: '12px 16px',
                    cursor: 'pointer',
                    background: collapsed ? 'transparent' : token.colorFillAlter,
                    borderBottom: collapsed
                      ? 'none'
                      : `1px solid ${token.colorBorderSecondary}`,
                    fontSize: token.fontSize,
                  }}
                >
                  <Col {...colSpan.name}>
                    <Flex align="center" gap={10} style={{ paddingRight: 8, minWidth: 0 }}>
                      <Typography.Text
                        type="secondary"
                        style={{
                          fontSize: token.fontSizeSM,
                          width: 12,
                          display: 'inline-block',
                          flexShrink: 0,
                        }}
                      >
                        {collapsed ? <RightOutlined /> : <DownOutlined />}
                      </Typography.Text>
                      <AppIconAvatar
                        name={appTitle}
                        fallbackColorSeed={app.name}
                        iconUrl={app.iconUrl}
                        size={38}
                      />
                      <div style={{ minWidth: 0, flex: 1 }}>
                        <Flex align="center" gap={4} style={{ minWidth: 0 }}>
                          <Typography.Text
                            strong
                            ellipsis={{ tooltip: appTitle }}
                            style={{ fontSize: token.fontSize }}
                          >
                            {appTitle}
                          </Typography.Text>
                          {renderEditIconBtn((e) => {
                            e.stopPropagation()
                            setEditingTarget({ appKey: app.key })
                          })}
                        </Flex>
                        <Flex wrap="wrap" align="center" gap={6} style={{ marginTop: 3 }}>
                          {hasAppAlias && (
                            <Typography.Text
                              type="secondary"
                              style={{
                                fontSize: token.fontSizeSM,
                                fontFamily: token.fontFamilyCode,
                              }}
                            >
                              {app.name}
                            </Typography.Text>
                          )}
                          <Tag variant="filled" color="blue" style={{ margin: 0, flexShrink: 0 }}>
                            {t.containers.projectTag.replace('{count}', String(app.totalCount))}
                          </Tag>
                          {renderUserTags(app.meta?.tags)}
                        </Flex>
                      </div>
                    </Flex>
                  </Col>
                  {showHostCol && <Col {...colSpan.host}>{renderHostCell(app.hostName)}</Col>}
                  <Col {...colSpan.state}>
                    <Badge
                      status={allRunning ? 'success' : someRunning ? 'warning' : 'default'}
                      text={
                        <Typography.Text
                          style={{
                            fontSize: token.fontSizeSM,
                            fontWeight: 500,
                            color: allRunning
                              ? token.colorSuccess
                              : someRunning
                                ? token.colorWarning
                                : token.colorTextSecondary,
                          }}
                        >
                          {t.containers.runningBadge
                            .replace('{running}', String(app.runningCount))
                            .replace('{total}', String(app.totalCount))}
                        </Typography.Text>
                      }
                    />
                  </Col>
                  <Col {...colSpan.uptime}>
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {formatProjectUptime(app)}
                    </Typography.Text>
                  </Col>
                  <Col {...colSpan.resource}>
                    {renderResourceCell(
                      app.runningCount > 0,
                      statsReady,
                      app.cpuPercent,
                      app.memUsage,
                      true,
                    )}
                  </Col>
                  <Col {...colSpan.image}>
                    {uniqueImages.length === 1 ? (
                      renderImageCell(uniqueImages[0])
                    ) : (
                      <Tooltip
                        title={
                          <div
                            style={{
                              fontFamily: token.fontFamilyCode,
                              fontSize: token.fontSizeSM,
                            }}
                          >
                            {uniqueImages.map((img) => (
                              <div key={img}>{img}</div>
                            ))}
                          </div>
                        }
                      >
                        <Typography.Text
                          type="secondary"
                          style={{ fontSize: token.fontSizeSM, color: token.colorTextTertiary }}
                        >
                          {t.containers.projectImagesCount.replace(
                            '{count}',
                            String(uniqueImages.length),
                          )}
                        </Typography.Text>
                      </Tooltip>
                    )}
                  </Col>
                  <Col {...colSpan.action} style={{ textAlign: 'right' }}>
                    {!appUrl && !canDeleteProject ? (
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        -
                      </Typography.Text>
                    ) : (
                      <Space size={2} onClick={(e) => e.stopPropagation()}>
                        {renderOpenIconBtn(appUrl)}
                        {canDeleteProject && (
                          <Tooltip title={t.containers.deleteProjectBtn}>
                            <Button
                              type="text"
                              danger
                              size="small"
                              aria-label={t.containers.deleteProjectBtn}
                              icon={<DeleteOutlined />}
                              onClick={() => setDeletingApp(app)}
                            />
                          </Tooltip>
                        )}
                      </Space>
                    )}
                  </Col>
                </Row>

                {/* 展开的子容器行 */}
                {!collapsed &&
                  app.containers.map((c, idx) => {
                    const isLastChild = idx === app.containers.length - 1
                    const isRunning = c.state === 'running'
                    const cAlias = (c.meta?.alias || '').trim()
                    const cTitle = cAlias || c.name
                    const hasCAlias = Boolean(cAlias && cAlias !== c.name)
                    const cUrl = resolveContainerUrl(app.hostAddress, c)
                    const cMetaKey = c.metaKey || `${app.key}/ctr/${c.name}`
                    const hasSubLine =
                      hasCAlias ||
                      Boolean(c.service && c.service !== c.name) ||
                      Boolean(c.meta?.tags && c.meta.tags.length > 0)

                    return (
                      <Row
                        key={c.id || c.name}
                        align="middle"
                        style={{
                          padding: '10px 16px',
                          background: token.colorFillQuaternary,
                          borderBottom: isLastChild
                            ? 'none'
                            : `1px solid ${token.colorBorderSecondary}`,
                          fontSize: token.fontSizeSM,
                        }}
                      >
                        <Col {...colSpan.name}>
                          <Flex
                            align="center"
                            gap={10}
                            style={{ paddingLeft: 26, paddingRight: 8, minWidth: 0 }}
                          >
                            <Typography.Text
                              type="secondary"
                              style={{
                                fontFamily: token.fontFamilyCode,
                                userSelect: 'none',
                                flexShrink: 0,
                              }}
                            >
                              {isLastChild ? '└─' : '├─'}
                            </Typography.Text>
                            <AppIconAvatar
                              name={cTitle}
                              fallbackColorSeed={c.name}
                              iconUrl={c.iconUrl}
                              size={32}
                            />
                            <div style={{ minWidth: 0, flex: 1 }}>
                              <Flex align="center" gap={4} style={{ minWidth: 0 }}>
                                <Typography.Text
                                  style={{ fontWeight: 500, fontSize: token.fontSizeSM }}
                                  ellipsis={{ tooltip: cTitle }}
                                >
                                  {cTitle}
                                </Typography.Text>
                                {renderEditIconBtn(() =>
                                  setEditingTarget({
                                    appKey: app.key,
                                    containerMetaKey: cMetaKey,
                                  }),
                                )}
                              </Flex>
                              {hasSubLine && (
                                <Flex wrap="wrap" align="center" gap={6} style={{ marginTop: 2 }}>
                                  {hasCAlias && (
                                    <Typography.Text
                                      type="secondary"
                                      style={{
                                        fontSize: token.fontSizeSM,
                                        fontFamily: token.fontFamilyCode,
                                      }}
                                    >
                                      {c.name}
                                    </Typography.Text>
                                  )}
                                  {c.service && c.service !== c.name && (
                                    <Tag variant="filled" style={{ margin: 0, flexShrink: 0 }}>
                                      {c.service}
                                    </Tag>
                                  )}
                                  {renderUserTags(c.meta?.tags)}
                                </Flex>
                              )}
                            </div>
                          </Flex>
                        </Col>
                        {showHostCol && (
                          <Col {...colSpan.host}>{renderHostCell(app.hostName, true)}</Col>
                        )}
                        <Col {...colSpan.state}>{renderStateCell(c)}</Col>
                        <Col {...colSpan.uptime}>
                          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                            {formatUptime(c)}
                          </Typography.Text>
                        </Col>
                        <Col {...colSpan.resource}>
                          {renderResourceCell(isRunning, statsReady, c.cpuPercent, c.memUsage)}
                        </Col>
                        <Col {...colSpan.image}>{renderImageCell(c.image)}</Col>
                        <Col {...colSpan.action} style={{ textAlign: 'right' }}>
                          {renderOpenIconBtn(cUrl)}
                        </Col>
                      </Row>
                    )
                  })}
              </div>
            )
          })}
        </Card>
      )}

      <ContainerMetaModal
        open={Boolean(editingMetaContext)}
        context={editingMetaContext}
        allTags={allTagsPool}
        onClose={() => setEditingTarget(null)}
        onSaved={handleRefresh}
      />

      <DeleteAppModal
        open={Boolean(deletingApp)}
        appKey={deletingApp?.key ?? ''}
        appName={deletingApp?.name ?? ''}
        hostName={deletingApp?.hostName ?? ''}
        source={deletingApp?.source ?? 'standalone'}
        isProjectHint={
          Boolean(deletingApp) &&
          (deletingApp!.source === 'compose' ||
            deletingApp!.source === 'dsm_project' ||
            deletingApp!.containers.length > 1)
        }
        onClose={() => setDeletingApp(null)}
        onDeleted={handleRefresh}
      />
    </Space>
  )
}
