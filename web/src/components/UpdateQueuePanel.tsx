import {
  Badge,
  Button,
  Card,
  Flex,
  Progress,
  Segmented,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import { CloseOutlined, SyncOutlined } from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api, type LayerProgress, type UpdateJobItem } from '../api/client'
import { useOrderedQueueJobs } from '../hooks/useUpdateQueue'
import { useI18n } from '../i18n'
import { useUIStore, type ProgressStyle } from '../store/ui'

function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let val = bytes
  let idx = 0
  while (val >= 1024 && idx < units.length - 1) {
    val /= 1024
    idx++
  }
  return `${val.toFixed(idx > 1 ? 1 : 0)} ${units[idx]}`
}

function extractTagLabel(version?: string, imageRef?: string): string {
  if (version && version.trim()) return version.trim()
  if (!imageRef) return ''
  const withoutDigest = imageRef.split('@')[0]
  const lastSlash = withoutDigest.lastIndexOf('/')
  const colonIdx = withoutDigest.indexOf(':', lastSlash + 1)
  if (colonIdx >= 0) {
    return withoutDigest.slice(colonIdx + 1)
  }
  return 'latest'
}

interface UpdateQueuePanelProps {
  mode: 'inline' | 'docked' | 'popover'
  onCollapse?: () => void
}

export default function UpdateQueuePanel({ mode, onCollapse }: UpdateQueuePanelProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const qc = useQueryClient()

  const jobs = useOrderedQueueJobs()
  const progressStyle = useUIStore((s) => s.progressStyle)
  const setProgressStyle = useUIStore((s) => s.setProgressStyle)
  const setQueueCollapsed = useUIStore((s) => s.setQueueCollapsed)
  const setActiveLogJobId = useUIStore((s) => s.setActiveLogJobId)
  const dismissQueueJob = useUIStore((s) => s.dismissQueueJob)
  const clearFinishedQueueJobs = useUIStore((s) => s.clearFinishedQueueJobs)

  if (jobs.length === 0) return null

  const anyRunning = jobs.some((j) => j.status === 'queued' || j.status === 'running')
  const hasFinished = jobs.some((j) => j.status === 'succeeded' || j.status === 'failed')

  const handleSwitchStyle = (next: ProgressStyle) => {
    if (next === progressStyle) return
    setProgressStyle(next)
    ;(async () => {
      try {
        const cfg = await api.getSettings()
        await api.saveSettings({ ...cfg, progressStyle: next })
        qc.invalidateQueries({ queryKey: ['settings'] })
      } catch {
        // localStorage already updated for instant persistence
      }
    })()
  }

  const formatPhaseText = (job: UpdateJobItem): string => {
    if (job.status === 'queued') return t.queue.statusQueued
    if (job.status === 'succeeded') return t.queue.statusSucceeded
    if (job.status === 'failed') return job.errorMsg || job.errorCode || t.queue.statusFailed
    switch (job.phase || job.progress?.phase) {
      case 'precheck':
        return t.home.phasePrecheck
      case 'pulling':
        return t.home.phasePulling
      case 'recreating':
        return t.home.phaseRecreating
      case 'verifying':
        return t.home.phaseVerifying
      case 'done':
        return t.home.phaseDone
      default:
        return job.progress?.statusText || t.home.updating
    }
  }

  const formatLayerStatus = (layer: LayerProgress, isDone: boolean): string => {
    if (isDone) return t.queue.layerDone
    switch (layer.status) {
      case 'downloading':
        return t.queue.layerDownloading
      case 'extracting':
        return t.queue.layerExtracting
      case 'done':
        return t.queue.layerDone
      default:
        return t.queue.layerWaiting
    }
  }

  const handleCollapse = () => {
    if (onCollapse) {
      onCollapse()
    } else {
      setQueueCollapsed(true)
    }
  }

  const isCompactWidth = mode === 'docked' || mode === 'popover'

  return (
    <Card
      size="small"
      variant={mode === 'popover' ? 'borderless' : 'outlined'}
      styles={{
        body: {
          padding: mode === 'popover' ? '4px 2px' : '12px 14px',
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
        },
      }}
      style={{
        borderColor: mode !== 'popover' && anyRunning ? token.colorPrimaryBorder : undefined,
        boxShadow:
          mode === 'docked'
            ? token.boxShadowSecondary
            : mode === 'popover'
              ? 'none'
              : undefined,
        background: mode === 'popover' ? 'transparent' : token.colorBgContainer,
      }}
    >
      {/* 队列标题栏 */}
      <Flex wrap="wrap" align="center" justify="space-between" gap={8}>
        <Space size={8} align="center" style={{ minWidth: 0 }}>
          {anyRunning ? (
            <SyncOutlined spin style={{ color: token.colorPrimary }} />
          ) : (
            <Badge status={jobs.some((j) => j.status === 'failed') ? 'error' : 'success'} />
          )}
          <Typography.Text strong>
            {t.queue.panelTitle.replace('{count}', String(jobs.length))}
          </Typography.Text>
        </Space>

        <Space size={6} align="center" style={{ flexShrink: 0 }}>
          {/* 整体 | 分层 切换（悬停满 2 秒弹出解释气泡） */}
          <Segmented
            size="small"
            value={progressStyle}
            onChange={(val) => handleSwitchStyle(val as ProgressStyle)}
            options={[
              {
                value: 'unified',
                label: (
                  <Tooltip mouseEnterDelay={2} title={t.queue.unifiedTip}>
                    <span>{t.queue.modeUnified}</span>
                  </Tooltip>
                ),
              },
              {
                value: 'layered',
                label: (
                  <Tooltip mouseEnterDelay={2} title={t.queue.layeredTip}>
                    <span>{t.queue.modeLayered}</span>
                  </Tooltip>
                ),
              },
            ]}
          />

          {hasFinished && (
            <Button size="small" type="text" onClick={clearFinishedQueueJobs}>
              {t.queue.clearFinished}
            </Button>
          )}

          <Button size="small" onClick={handleCollapse}>
            {t.queue.collapseBtn}
          </Button>
        </Space>
      </Flex>

      {/* 任务行列表 */}
      <Flex
        vertical
        gap={8}
        style={{
          maxHeight: isCompactWidth ? 'calc(100vh - 180px)' : 320,
          overflowY: 'auto',
        }}
      >
        {jobs.map((job) => {
          const displayName =
            job.serviceName && job.serviceName !== 'main' && job.serviceName !== job.appName
              ? `${job.appName} / ${job.serviceName}`
              : job.appName
          const oldVer = extractTagLabel(job.oldVersion, job.oldImageRef)
          const tgtVer = extractTagLabel(job.targetVersion, job.targetImageRef)
          const layers = job.progress?.layers ?? []
          const showLayered = progressStyle === 'layered' && layers.length > 0
          const doneLayers =
            job.status === 'succeeded'
              ? layers.length
              : layers.filter((l) => l.status === 'done' || (l.percent ?? 0) >= 100).length
          const unifiedPct =
            job.status === 'succeeded'
              ? 100
              : Math.max(
                  0,
                  Math.min(100, job.progress?.percent ?? (job.status === 'running' ? 5 : 0)),
                )
          const isFinished = job.status === 'succeeded' || job.status === 'failed'
          const phaseText = formatPhaseText(job)
          const hasBytes =
            job.status === 'running' && (job.progress?.totalBytes ?? 0) > 0

          return (
            <Card
              key={job.id}
              size="small"
              styles={{
                body: {
                  padding: '10px 12px',
                  display: 'flex',
                  flexDirection: 'column',
                  gap: 6,
                },
              }}
              style={{
                background: token.colorFillAlter,
                borderColor:
                  job.status === 'failed' ? token.colorErrorBorder : token.colorBorderSecondary,
              }}
            >
              {/* 第一行：应用名 + 版本号变化 + 右侧独立「详情」按钮 */}
              <Flex align="center" justify="space-between" gap={8}>
                <Flex
                  align="center"
                  gap={6}
                  wrap={isCompactWidth ? 'nowrap' : 'wrap'}
                  style={{ minWidth: 0, flex: 1 }}
                >
                  <Typography.Text
                    strong
                    ellipsis={{ tooltip: `${displayName} (${job.hostName})` }}
                    style={{ fontSize: token.fontSizeSM }}
                  >
                    {displayName}
                  </Typography.Text>
                  {(oldVer || tgtVer) && (
                    <Tag
                      variant="filled"
                      style={{
                        margin: 0,
                        fontFamily: token.fontFamilyCode,
                        fontSize: 11,
                        maxWidth: '100%',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        flexShrink: isCompactWidth ? 1 : 0,
                      }}
                    >
                      {oldVer ? `${oldVer} → ${tgtVer}` : tgtVer}
                    </Tag>
                  )}
                </Flex>

                <Space size={4} style={{ flexShrink: 0 }}>
                  <Button size="small" onClick={() => setActiveLogJobId(job.id)}>
                    {t.queue.detailBtn}
                  </Button>
                  {isFinished && (
                    <Tooltip title={t.queue.dismissTip}>
                      <Button
                        size="small"
                        type="text"
                        icon={<CloseOutlined />}
                        aria-label={t.queue.dismissTip}
                        onClick={() => dismissQueueJob(job.id)}
                      />
                    </Tooltip>
                  )}
                </Space>
              </Flex>

              {/* 第二行：阶段文案 / 字节进度 / 失败摘要 */}
              <Flex align="center" justify="space-between" gap={8}>
                <Typography.Text
                  type={
                    job.status === 'failed'
                      ? 'danger'
                      : job.status === 'succeeded'
                        ? 'success'
                        : 'secondary'
                  }
                  strong={job.status === 'succeeded' || job.status === 'failed'}
                  ellipsis={{ tooltip: phaseText }}
                  style={{ fontSize: token.fontSizeSM, minWidth: 0, flex: 1 }}
                >
                  {phaseText}
                </Typography.Text>
                {hasBytes && (
                  <Typography.Text
                    type="secondary"
                    style={{
                      fontFamily: token.fontFamilyCode,
                      fontSize: 11,
                      flexShrink: 0,
                    }}
                  >
                    {formatBytes(job.progress.downloadedBytes)} /{' '}
                    {formatBytes(job.progress.totalBytes)}
                  </Typography.Text>
                )}
              </Flex>

              {/* 第三行：分层细条（默认）或 整体单条 */}
              {showLayered ? (
                <Flex align="center" gap={8}>
                  <Flex align="center" gap={3} style={{ flex: 1, minWidth: 0 }}>
                    {layers.map((layer, idx) => {
                      const isDone =
                        job.status === 'succeeded' ||
                        layer.status === 'done' ||
                        (layer.percent ?? 0) >= 100
                      const pct = isDone
                        ? 100
                        : Math.max(0, Math.min(100, layer.percent ?? 0))
                      const fillBg =
                        job.status === 'failed' && !isDone
                          ? token.colorError
                          : isDone
                            ? token.colorSuccess
                            : token.colorPrimary
                      const sizeInfo =
                        layer.total > 0
                          ? ` · ${formatBytes(layer.current)} / ${formatBytes(layer.total)} (${Math.round(pct)}%)`
                          : pct > 0 && !isDone
                            ? ` (${Math.round(pct)}%)`
                            : ''
                      const tip = `${t.queue.layerIdLabel} ${layer.id || idx + 1}: ${formatLayerStatus(layer, isDone)}${sizeInfo}`

                      return (
                        <Tooltip key={layer.id || idx} title={tip} mouseEnterDelay={0.15}>
                          <div
                            title={tip}
                            style={{
                              flex: 1,
                              minWidth: 3,
                              height: 8,
                              borderRadius: token.borderRadiusXS,
                              background: token.colorFillSecondary,
                              overflow: 'hidden',
                            }}
                          >
                            <div
                              style={{
                                width: `${pct}%`,
                                height: '100%',
                                borderRadius: token.borderRadiusXS,
                                background: fillBg,
                                transition: 'width 0.22s ease, background-color 0.2s ease',
                              }}
                            />
                          </div>
                        </Tooltip>
                      )
                    })}
                  </Flex>
                  <Typography.Text
                    type="secondary"
                    style={{
                      fontFamily: token.fontFamilyCode,
                      fontSize: 11,
                      flexShrink: 0,
                    }}
                  >
                    {doneLayers}/{layers.length}
                  </Typography.Text>
                </Flex>
              ) : (
                <Progress
                  size="small"
                  percent={Math.round(unifiedPct)}
                  status={
                    job.status === 'failed'
                      ? 'exception'
                      : job.status === 'succeeded'
                        ? 'success'
                        : job.status === 'running'
                          ? 'active'
                          : 'normal'
                  }
                  style={{ margin: 0 }}
                />
              )}
            </Card>
          )
        })}
      </Flex>
    </Card>
  )
}
