import { useMemo, useState } from 'react'
import {
  Avatar,
  Badge,
  Button,
  Card,
  Empty,
  Flex,
  Input,
  Segmented,
  Space,
  Spin,
  Tag,
  Typography,
  theme,
} from 'antd'
import {
  CaretDownOutlined,
  CaretRightOutlined,
  ContainerOutlined,
  ExpandAltOutlined,
  FolderOpenOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  ShrinkOutlined,
} from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, type AppRuntimeTree, type ContainerRuntimeStat } from '../api/client'
import { useI18n } from '../i18n'

const avatarColors = ['#2f6bff', '#13c2c2', '#722ed1', '#eb2f96', '#fa8c16', '#52c41a']

function pickColor(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) {
    h = (h * 31 + name.charCodeAt(i)) | 0
  }
  return avatarColors[Math.abs(h) % avatarColors.length]
}

function formatMem(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 MB'
  const mb = bytes / (1024 * 1024)
  if (mb >= 1024) {
    return `${(mb / 1024).toFixed(2)} GB`
  }
  return `${mb >= 10 ? mb.toFixed(0) : mb.toFixed(1)} MB`
}

export default function ContainersPage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const navigate = useNavigate()

  const [hostFilter, setHostFilter] = useState<string>('')
  const [stateFilter, setStateFilter] = useState<'all' | 'running' | 'stopped'>('all')
  const [keyword, setKeyword] = useState('')
  const [collapsedKeys, setCollapsedKeys] = useState<Record<string, boolean>>({})

  const hostsQuery = useQuery({
    queryKey: ['hosts', locale],
    queryFn: api.listHosts,
  })
  const containersQuery = useQuery({
    queryKey: ['containers', hostFilter, locale],
    queryFn: () => api.listContainers(hostFilter || undefined),
    refetchInterval: 10000,
  })

  const hosts = hostsQuery.data?.items ?? []
  const rawTrees = containersQuery.data?.items ?? []

  // 统计各状态容器数量（用于状态筛选标签）
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

  // 根据状态与搜索词过滤树节点
  const filteredTrees = useMemo(() => {
    const q = keyword.trim().toLowerCase()
    const result: AppRuntimeTree[] = []
    for (const app of rawTrees) {
      const appNameMatches = q ? app.name.toLowerCase().includes(q) : true
      const matchedContainers = app.containers.filter((c) => {
        const isRunning = c.state === 'running'
        if (stateFilter === 'running' && !isRunning) return false
        if (stateFilter === 'stopped' && isRunning) return false
        if (!q) return true
        if (appNameMatches) return true
        return (
          c.name.toLowerCase().includes(q) ||
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
  }, [rawTrees, stateFilter, keyword])

  const multiContainerKeys = useMemo(
    () => filteredTrees.filter((a) => a.containers.length > 1 || a.source === 'compose' || a.source === 'dsm_project').map((a) => a.key),
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

  const formatUptime = (c: ContainerRuntimeStat): string => {
    if (c.state !== 'running') {
      return t.containers.stateStopped
    }
    if (c.startedAt && c.startedAt > 0) {
      const nowSec = Math.floor(Date.now() / 1000)
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
    if (c.uptime) return c.uptime
    return t.containers.stateRunning
  }

  const sourceTag = (source: AppRuntimeTree['source']) => {
    switch (source) {
      case 'compose':
        return <Tag color="blue">{t.home.tagCompose}</Tag>
      case 'dsm_project':
        return <Tag color="geekblue">{t.home.tagDSM}</Tag>
      case 'vendor_managed':
        return <Tag color="purple">{t.home.tagVendor}</Tag>
      default:
        return <Tag>{t.home.tagStandalone}</Tag>
    }
  }

  const renderStateBadge = (c: ContainerRuntimeStat) => {
    if (c.state === 'running') {
      return (
        <Space size={4}>
          <Badge status="success" text={t.containers.stateRunning} />
          {c.health === 'healthy' && (
            <Tag color="success" style={{ margin: 0, fontSize: 11 }}>
              {t.containers.healthHealthy}
            </Tag>
          )}
          {c.health === 'unhealthy' && (
            <Tag color="error" style={{ margin: 0, fontSize: 11 }}>
              {t.containers.healthUnhealthy}
            </Tag>
          )}
          {c.health === 'starting' && (
            <Tag color="processing" style={{ margin: 0, fontSize: 11 }}>
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

  const renderContainerMetrics = (c: ContainerRuntimeStat) => {
    const isRunning = c.state === 'running'
    return (
      <Space size={16} wrap style={{ justifyContent: 'flex-end' }}>
        {renderStateBadge(c)}
        <Typography.Text type="secondary" style={{ fontSize: 12, minWidth: 110, display: 'inline-block' }}>
          {formatUptime(c)}
        </Typography.Text>
        <Typography.Text style={{ fontSize: 12, fontFamily: 'monospace', minWidth: 78, display: 'inline-block' }}>
          {t.containers.cpuLabel}: {isRunning ? `${c.cpuPercent.toFixed(1)}%` : '-'}
        </Typography.Text>
        <Typography.Text style={{ fontSize: 12, fontFamily: 'monospace', minWidth: 105, display: 'inline-block' }}>
          {t.containers.memLabel}: {isRunning ? formatMem(c.memUsage) : '-'}
        </Typography.Text>
        <Typography.Text
          code
          ellipsis={{ tooltip: c.image }}
          style={{ fontSize: 12, maxWidth: 240 }}
        >
          {c.image}
        </Typography.Text>
      </Space>
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
        <Typography.Title level={3} style={{ margin: 0 }}>
          {t.containers.title}
        </Typography.Title>
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
            icon={<ReloadOutlined spin={containersQuery.isFetching} />}
            loading={containersQuery.isFetching}
            onClick={() => containersQuery.refetch()}
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

      {containersQuery.isPending ? (
        <Flex justify="center" style={{ padding: 48 }}>
          <Spin />
        </Flex>
      ) : filteredTrees.length === 0 ? (
        <Card>
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t.containers.empty} />
        </Card>
      ) : (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          {filteredTrees.map((app) => {
            const isProject =
              app.containers.length > 1 ||
              app.source === 'compose' ||
              app.source === 'dsm_project'
            const collapsed = Boolean(collapsedKeys[app.key])

            if (!isProject && app.containers.length === 1) {
              const c = app.containers[0]
              return (
                <Card key={app.key} size="small" styles={{ body: { padding: '12px 16px' } }}>
                  <Flex justify="space-between" align="center" wrap gap={12}>
                    <Space size={10} wrap>
                      <Avatar
                        size={32}
                        shape="square"
                        style={{
                          backgroundColor: pickColor(app.name),
                          borderRadius: 8,
                          fontWeight: 600,
                        }}
                      >
                        {app.name.slice(0, 2).toUpperCase()}
                      </Avatar>
                      <Typography.Text strong style={{ fontSize: 15 }}>
                        {c.name || app.name}
                      </Typography.Text>
                      {sourceTag(app.source)}
                      <Tag variant="filled" style={{ margin: 0 }}>
                        {app.hostName}
                      </Tag>
                    </Space>
                    {renderContainerMetrics(c)}
                  </Flex>
                </Card>
              )
            }

            const allRunning = app.runningCount === app.totalCount && app.totalCount > 0
            const someRunning = app.runningCount > 0 && app.runningCount < app.totalCount

            return (
              <Card key={app.key} size="small" styles={{ body: { padding: 0 } }}>
                {/* 父节点：多容器项目头 */}
                <Flex
                  justify="space-between"
                  align="center"
                  wrap
                  gap={12}
                  onClick={() => toggleOne(app.key)}
                  style={{
                    padding: '12px 16px',
                    cursor: 'pointer',
                    background: collapsed ? 'transparent' : token.colorFillQuaternary,
                    borderBottom: collapsed ? 'none' : `1px solid ${token.colorBorderSecondary}`,
                  }}
                >
                  <Space size={10} wrap>
                    <Typography.Text type="secondary">
                      {collapsed ? <CaretRightOutlined /> : <CaretDownOutlined />}
                    </Typography.Text>
                    <Avatar
                      size={32}
                      shape="square"
                      icon={<FolderOpenOutlined />}
                      style={{
                        backgroundColor: pickColor(app.name),
                        borderRadius: 8,
                      }}
                    />
                    <Typography.Text strong style={{ fontSize: 15 }}>
                      {app.name}
                    </Typography.Text>
                    {sourceTag(app.source)}
                    <Tag variant="filled" style={{ margin: 0 }}>
                      {app.hostName}
                    </Tag>
                  </Space>

                  <Space size={16} wrap>
                    <Tag
                      color={allRunning ? 'success' : someRunning ? 'warning' : 'default'}
                      style={{ margin: 0 }}
                    >
                      ●{' '}
                      {t.containers.runningBadge
                        .replace('{running}', String(app.runningCount))
                        .replace('{total}', String(app.totalCount))}
                    </Tag>
                    <Typography.Text
                      strong
                      style={{ fontSize: 12, fontFamily: 'monospace', minWidth: 78, display: 'inline-block' }}
                    >
                      {t.containers.cpuLabel}: {app.runningCount > 0 ? `${app.cpuPercent.toFixed(1)}%` : '-'}
                    </Typography.Text>
                    <Typography.Text
                      strong
                      style={{ fontSize: 12, fontFamily: 'monospace', minWidth: 105, display: 'inline-block' }}
                    >
                      {t.containers.memLabel}: {app.runningCount > 0 ? formatMem(app.memUsage) : '-'}
                    </Typography.Text>
                  </Space>
                </Flex>

                {/* 子节点：树状连线展示项目下的每个容器 */}
                {!collapsed && (
                  <div style={{ padding: '4px 16px 8px 36px' }}>
                    {app.containers.map((c, idx) => {
                      const isLast = idx === app.containers.length - 1
                      return (
                        <Flex
                          key={c.id || c.name}
                          justify="space-between"
                          align="center"
                          wrap
                          gap={12}
                          style={{
                            padding: '8px 0',
                            borderBottom: isLast
                              ? 'none'
                              : `1px dashed ${token.colorBorderSecondary}`,
                          }}
                        >
                          <Space size={8} wrap>
                            <Typography.Text
                              type="secondary"
                              style={{ fontFamily: 'monospace', userSelect: 'none' }}
                            >
                              {isLast ? '└─' : '├─'}
                            </Typography.Text>
                            <ContainerOutlined style={{ color: token.colorTextSecondary }} />
                            <Typography.Text strong>{c.name}</Typography.Text>
                            {c.service && c.service !== c.name && (
                              <Tag style={{ margin: 0, fontSize: 11 }}>{c.service}</Tag>
                            )}
                          </Space>
                          {renderContainerMetrics(c)}
                        </Flex>
                      )
                    })}
                  </div>
                )}
              </Card>
            )
          })}
        </Space>
      )}
    </Space>
  )
}
