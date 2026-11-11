import {
  Button,
  Card,
  Dropdown,
  Flex,
  Progress,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import {
  CheckCircleFilled,
  CheckOutlined,
  CloseCircleFilled,
  SettingOutlined,
  SyncOutlined,
  UpOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api, type LayerProgress, type OverviewResponse, type UpdateJobItem } from '../api/client'
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
  const clearFinishedQueueJobs = useUIStore((s) => s.clearFinishedQueueJobs)

  if (jobs.length === 0) return null

  const anyRunning = jobs.some((j) => j.status === 'queued' || j.status === 'running')

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
    clearFinishedQueueJobs()
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
        <Space size={6} align="center" style={{ minWidth: 0 }}>
          {anyRunning ? (
            <SyncOutlined spin style={{ color: token.colorPrimary }} />
          ) : jobs.some((j) => j.status === 'failed') ? (
            <CloseCircleFilled style={{ color: token.colorError }} />
          ) : (
            <CheckCircleFilled style={{ color: token.colorSuccess }} />
          )}
          <Typography.Text strong>
            {t.queue.panelTitle.replace('{count}', String(jobs.length))}
          </Typography.Text>
        </Space>

        <Space size={6} align="center" style={{ flexShrink: 0 }}>
          {/* 官方圆形设置按钮 + 分组直选下拉框 */}
          <Dropdown
            trigger={['click']}
            placement="bottomRight"
            menu={{
              selectedKeys: [progressStyle],
              items: [
                {
                  key: 'progress-style-group',
                  type: 'group',
                  label: t.queue.progressStyleLabel,
                  children: [
                    {
                      key: 'layered',
                      label: (
                        <Tooltip mouseEnterDelay={2} placement="left" title={t.queue.layeredTip}>
                          <Flex align="center" justify="space-between" gap={20}>
                            <span>{t.queue.modeLayered}</span>
                            <CheckOutlined
                              style={{
                                fontSize: 12,
                                color: token.colorPrimary,
                                visibility: progressStyle === 'layered' ? 'visible' : 'hidden',
                              }}
                            />
                          </Flex>
                        </Tooltip>
                      ),
                      onClick: () => handleSwitchStyle('layered'),
                    },
                    {
                      key: 'unified',
                      label: (
                        <Tooltip mouseEnterDelay={2} placement="left" title={t.queue.unifiedTip}>
                          <Flex align="center" justify="space-between" gap={20}>
                            <span>{t.queue.modeUnified}</span>
                            <CheckOutlined
                              style={{
                                fontSize: 12,
                                color: token.colorPrimary,
                                visibility: progressStyle === 'unified' ? 'visible' : 'hidden',
                              }}
                            />
                          </Flex>
                        </Tooltip>
                      ),
                      onClick: () => handleSwitchStyle('unified'),
                    },
                  ],
                },
              ],
            }}
          >
            <Button
              size="small"
              shape="circle"
              icon={<SettingOutlined />}
              aria-label={t.queue.progressStyleLabel}
            />
          </Dropdown>

          <Button
            size="small"
            icon={<UpOutlined style={{ fontSize: 10 }} />}
            iconPlacement="end"
            onClick={handleCollapse}
          >
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
          const cachedOverviews = qc.getQueriesData<OverviewResponse>({ queryKey: ['overview'] })
          let knownMultiContainer: boolean | undefined
          for (const [, ov] of cachedOverviews) {
            if (!ov) continue
            for (const list of [ov.normal, ov.caution, ov.noUpdate]) {
              const found = list?.find((a) => a.key === job.appKey || a.name === job.appName)
              if (found) {
                const cnt =
                  found.totalServiceCount ?? found.allServices?.length ?? found.services.length
                knownMultiContainer = cnt >= 2
                break
              }
            }
            if (knownMultiContainer !== undefined) break
          }
          const shouldAppendSvc =
            Boolean(job.serviceName) &&
            job.serviceName !== 'main' &&
            job.serviceName !== job.appName &&
            (knownMultiContainer === true ||
              (knownMultiContainer === undefined && job.serviceName !== 'app'))
          const displayName = shouldAppendSvc
            ? `${job.appName} / ${job.serviceName}`
            : job.appName
          const oldVer = extractTagLabel(job.oldVersion, job.oldImageRef)
          const tgtVer = extractTagLabel(job.targetVersion, job.targetImageRef)
          const verText = oldVer ? `${oldVer} → ${tgtVer}` : tgtVer
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
              {/* 第一行：应用名 + 完整版本号变化（短文本同行、长文本自动换行，两侧均 0 截断） + 右侧「详情」按钮 */}
              <Flex align="flex-start" justify="space-between" gap={8}>
                <Flex
                  align="center"
                  gap={6}
                  wrap="wrap"
                  style={{ minWidth: 0, flex: 1 }}
                >
                  <Typography.Text
                    strong
                    ellipsis={{ tooltip: `${displayName} (${job.hostName})` }}
                    style={{ fontSize: token.fontSizeSM, maxWidth: '100%' }}
                  >
                    {displayName}
                  </Typography.Text>
                  {verText && (
                    <Tooltip title={verText.length > 32 ? verText : undefined}>
                      <Tag
                        variant="filled"
                        style={{
                          margin: 0,
                          fontFamily: token.fontFamilyCode,
                          fontSize: 12,
                          maxWidth: '100%',
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          flexShrink: 0,
                        }}
                      >
                        {verText}
                      </Tag>
                    </Tooltip>
                  )}
                </Flex>

                <Button
                  size="small"
                  style={{ flexShrink: 0 }}
                  onClick={() => setActiveLogJobId(job.id)}
                >
                  {t.queue.detailBtn}
                </Button>
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
