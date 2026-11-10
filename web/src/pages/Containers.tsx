import { useMemo, useState } from 'react'
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
  DownOutlined,
  ExpandAltOutlined,
  PlusOutlined,
  ReloadOutlined,
  RightOutlined,
  SearchOutlined,
  ShrinkOutlined,
} from '@ant-design/icons'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, type AppRuntimeTree, type ContainerRuntimeStat } from '../api/client'
import { useI18n } from '../i18n'

const badgePalette = [
  { bg: 'rgba(114, 46, 209, 0.14)', fg: '#722ed1' },
  { bg: 'rgba(47, 107, 255, 0.14)', fg: '#2f6bff' },
  { bg: 'rgba(19, 194, 194, 0.14)', fg: '#13c2c2' },
  { bg: 'rgba(235, 47, 150, 0.14)', fg: '#eb2f96' },
  { bg: 'rgba(250, 140, 22, 0.14)', fg: '#fa8c16' },
  { bg: 'rgba(82, 196, 26, 0.14)', fg: '#52c41a' },
]

function pickBadgeStyle(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) {
    h = (h * 31 + name.charCodeAt(i)) | 0
  }
  return badgePalette[Math.abs(h) % badgePalette.length]
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
    return t.containers.stateRunning
  }

  const formatProjectUptime = (app: AppRuntimeTree): string => {
    const runningList = app.containers.filter((c) => c.state === 'running')
    if (runningList.length === 0) return t.containers.stateStopped
    // 取最早启动的运行中容器计算项目运行时长
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
            <Tag color="success" style={{ margin: 0, fontSize: 11, lineHeight: '16px' }}>
              {t.containers.healthHealthy}
            </Tag>
          )}
          {c.health === 'unhealthy' && (
            <Tag color="error" style={{ margin: 0, fontSize: 11, lineHeight: '16px' }}>
              {t.containers.healthUnhealthy}
            </Tag>
          )}
          {c.health === 'starting' && (
            <Tag color="processing" style={{ margin: 0, fontSize: 11, lineHeight: '16px' }}>
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

  const renderInitialBadge = (name: string) => {
    const st = pickBadgeStyle(name)
    return (
      <span
        style={{
          width: 24,
          height: 24,
          borderRadius: 6,
          background: st.bg,
          color: st.fg,
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          fontWeight: 700,
          fontSize: 11,
          flexShrink: 0,
        }}
      >
        {name.slice(0, 2).toUpperCase()}
      </span>
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
        <div
          style={{
            border: `1px solid ${token.colorBorderSecondary}`,
            borderRadius: 12,
            overflow: 'hidden',
            background: token.colorBgContainer,
          }}
        >
          {/* 统一表头（5 列：8 / 4 / 4 / 4 / 4，对应 12 栅格的 4 / 2 / 2 / 2 / 2） */}
          <Row
            align="middle"
            style={{
              padding: '10px 16px',
              background: token.colorFillQuaternary,
              borderBottom: `1px solid ${token.colorBorderSecondary}`,
              fontSize: 12,
              fontWeight: 500,
              color: token.colorTextSecondary,
            }}
          >
            <Col xs={12} md={8}>
              {t.containers.colName}
            </Col>
            <Col xs={6} md={4}>
              {t.containers.colState}
            </Col>
            <Col xs={0} md={4}>
              {t.containers.colUptime}
            </Col>
            <Col xs={6} md={4}>
              {t.containers.colResource}
            </Col>
            <Col xs={0} md={4} style={{ textAlign: 'right' }}>
              {t.containers.colImage}
            </Col>
          </Row>

          {/* 树状行列表 */}
          {filteredTrees.map((app, appIdx) => {
            const isProject = app.containers.length > 1
            const collapsed = Boolean(collapsedKeys[app.key])
            const isLastApp = appIdx === filteredTrees.length - 1

            // 单容器项目行
            if (!isProject && app.containers.length === 1) {
              const c = app.containers[0]
              const isRunning = c.state === 'running'
              return (
                <Row
                  key={app.key}
                  align="middle"
                  style={{
                    padding: '12px 16px',
                    borderBottom: isLastApp
                      ? 'none'
                      : `1px solid ${token.colorBorderSecondary}`,
                    fontSize: 13,
                  }}
                >
                  <Col xs={12} md={8}>
                    <Flex align="center" gap={10} style={{ paddingLeft: 22, minWidth: 0 }}>
                      {renderInitialBadge(app.name)}
                      <Typography.Text strong ellipsis style={{ maxWidth: 180 }}>
                        {app.name}
                      </Typography.Text>
                      <Tag
                        color={
                          app.source === 'dsm_project'
                            ? 'geekblue'
                            : app.source === 'compose'
                              ? 'blue'
                              : app.source === 'vendor_managed'
                                ? 'purple'
                                : undefined
                        }
                        style={{
                          margin: 0,
                          fontSize: 11,
                          lineHeight: '18px',
                        }}
                      >
                        {app.source === 'dsm_project'
                          ? t.home.tagDSM
                          : app.source === 'compose'
                            ? t.home.tagCompose
                            : app.source === 'vendor_managed'
                              ? t.home.tagVendor
                              : t.home.tagStandalone}
                      </Tag>
                    </Flex>
                  </Col>
                  <Col xs={6} md={4}>
                    {renderStateCell(c)}
                  </Col>
                  <Col xs={0} md={4}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {formatUptime(c)}
                    </Typography.Text>
                  </Col>
                  <Col xs={6} md={4}>
                    <Typography.Text style={{ fontSize: 12, fontFamily: 'monospace' }}>
                      {isRunning
                        ? `${c.cpuPercent.toFixed(1)}% · ${formatMem(c.memUsage)}`
                        : '-'}
                    </Typography.Text>
                  </Col>
                  <Col xs={0} md={4} style={{ textAlign: 'right' }}>
                    <Tooltip title={`${c.image} (${app.hostName})`}>
                      <Typography.Text
                        type="secondary"
                        style={{
                          fontSize: 12,
                          fontFamily: 'monospace',
                          maxWidth: '100%',
                          display: 'inline-block',
                        }}
                        ellipsis
                      >
                        {c.image}
                      </Typography.Text>
                    </Tooltip>
                  </Col>
                </Row>
              )
            }

            // 多容器项目父节点 + 子容器行
            const allRunning = app.runningCount === app.totalCount && app.totalCount > 0
            const someRunning = app.runningCount > 0 && app.runningCount < app.totalCount

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
                    background: collapsed ? 'transparent' : token.colorFillQuaternary,
                    borderBottom: collapsed
                      ? 'none'
                      : `1px solid ${token.colorBorderSecondary}`,
                    fontSize: 13,
                  }}
                >
                  <Col xs={12} md={8}>
                    <Flex align="center" gap={10} style={{ minWidth: 0 }}>
                      <Typography.Text
                        type="secondary"
                        style={{ fontSize: 11, width: 12, display: 'inline-block' }}
                      >
                        {collapsed ? <RightOutlined /> : <DownOutlined />}
                      </Typography.Text>
                      {renderInitialBadge(app.name)}
                      <Typography.Text strong ellipsis style={{ maxWidth: 180 }}>
                        {app.name}
                      </Typography.Text>
                      <Tag
                        color="blue"
                        style={{
                          margin: 0,
                          fontSize: 11,
                          lineHeight: '18px',
                          border: 'none',
                        }}
                      >
                        {t.containers.projectTag.replace('{count}', String(app.totalCount))}
                      </Tag>
                    </Flex>
                  </Col>
                  <Col xs={6} md={4}>
                    <Badge
                      status={allRunning ? 'success' : someRunning ? 'warning' : 'default'}
                      text={
                        <span
                          style={{
                            fontSize: 12,
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
                        </span>
                      }
                    />
                  </Col>
                  <Col xs={0} md={4}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {formatProjectUptime(app)}
                    </Typography.Text>
                  </Col>
                  <Col xs={6} md={4}>
                    <Typography.Text strong style={{ fontSize: 12, fontFamily: 'monospace' }}>
                      {app.runningCount > 0
                        ? `${app.cpuPercent.toFixed(1)}% · ${formatMem(app.memUsage)}`
                        : '-'}
                    </Typography.Text>
                  </Col>
                  <Col xs={0} md={4} style={{ textAlign: 'right' }}>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }} ellipsis>
                      {app.hostName}
                    </Typography.Text>
                  </Col>
                </Row>

                {/* 展开的子容器行 */}
                {!collapsed &&
                  app.containers.map((c, idx) => {
                    const isLastChild = idx === app.containers.length - 1
                    const isRunning = c.state === 'running'
                    return (
                      <Row
                        key={c.id || c.name}
                        align="middle"
                        style={{
                          padding: '9px 16px',
                          background: token.colorBgLayout,
                          borderBottom: isLastChild
                            ? 'none'
                            : `1px solid ${token.colorBorderSecondary}`,
                          fontSize: 12,
                        }}
                      >
                        <Col xs={12} md={8}>
                          <Flex align="center" gap={8} style={{ paddingLeft: 28, minWidth: 0 }}>
                            <Typography.Text
                              type="secondary"
                              style={{ fontFamily: 'monospace', userSelect: 'none' }}
                            >
                              {isLastChild ? '└─' : '├─'}
                            </Typography.Text>
                            <Typography.Text
                              style={{ fontWeight: 500, fontSize: 12 }}
                              ellipsis={{ tooltip: c.name }}
                            >
                              {c.name}
                            </Typography.Text>
                            {c.service && c.service !== c.name && (
                              <Tag style={{ margin: 0, fontSize: 11, lineHeight: '16px' }}>
                                {c.service}
                              </Tag>
                            )}
                          </Flex>
                        </Col>
                        <Col xs={6} md={4}>
                          {renderStateCell(c)}
                        </Col>
                        <Col xs={0} md={4}>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {formatUptime(c)}
                          </Typography.Text>
                        </Col>
                        <Col xs={6} md={4}>
                          <Typography.Text style={{ fontSize: 12, fontFamily: 'monospace' }}>
                            {isRunning
                              ? `${c.cpuPercent.toFixed(1)}% · ${formatMem(c.memUsage)}`
                              : '-'}
                          </Typography.Text>
                        </Col>
                        <Col xs={0} md={4} style={{ textAlign: 'right' }}>
                          <Tooltip title={c.image}>
                            <Typography.Text
                              type="secondary"
                              style={{
                                fontSize: 12,
                                fontFamily: 'monospace',
                                maxWidth: '100%',
                                display: 'inline-block',
                              }}
                              ellipsis
                            >
                              {c.image}
                            </Typography.Text>
                          </Tooltip>
                        </Col>
                      </Row>
                    )
                  })}
              </div>
            )
          })}
        </div>
      )}
    </Space>
  )
}
