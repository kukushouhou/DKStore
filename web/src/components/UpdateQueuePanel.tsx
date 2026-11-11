import { Tooltip, theme } from 'antd'
import { SyncOutlined } from '@ant-design/icons'
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
    <div
      style={{
        borderRadius: 14,
        background: token.colorBgContainer,
        border:
          mode === 'popover'
            ? 'none'
            : `1px solid ${anyRunning ? 'rgba(37, 99, 235, 0.32)' : token.colorBorderSecondary}`,
        boxShadow:
          mode === 'docked'
            ? '0 10px 28px rgba(0, 0, 0, 0.08)'
            : mode === 'inline'
              ? '0 2px 10px rgba(0, 0, 0, 0.03)'
              : 'none',
        padding: mode === 'popover' ? '4px 2px' : '12px 14px',
        display: 'flex',
        flexDirection: 'column',
        gap: 10,
      }}
    >
      {/* 队列标题栏 */}
      <div
        style={{
          display: 'flex',
          flexWrap: 'wrap',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 8,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 7, minWidth: 0 }}>
          {anyRunning ? (
            <SyncOutlined spin style={{ color: token.colorPrimary, fontSize: 13 }} />
          ) : (
            <span
              style={{
                width: 8,
                height: 8,
                borderRadius: '50%',
                background: jobs.some((j) => j.status === 'failed') ? '#ef4444' : '#10b981',
                flexShrink: 0,
              }}
            />
          )}
          <span style={{ fontWeight: 600, fontSize: 13, color: token.colorText }}>
            {t.queue.panelTitle.replace('{count}', String(jobs.length))}
          </span>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
          {/* 整体 | 分层 切换（悬停满 2 秒弹出解释气泡） */}
          <div
            style={{
              display: 'inline-flex',
              padding: 2,
              borderRadius: 7,
              background: token.colorFillQuaternary,
              border: `1px solid ${token.colorBorderSecondary}`,
              gap: 1,
            }}
          >
            <Tooltip mouseEnterDelay={2} title={t.queue.unifiedTip}>
              <button
                type="button"
                onClick={() => handleSwitchStyle('unified')}
                style={{
                  padding: '2px 7px',
                  borderRadius: 5,
                  border: 'none',
                  fontSize: 11,
                  fontWeight: progressStyle === 'unified' ? 600 : 400,
                  cursor: 'pointer',
                  background: progressStyle === 'unified' ? token.colorPrimary : 'transparent',
                  color: progressStyle === 'unified' ? '#ffffff' : token.colorTextSecondary,
                  lineHeight: 1.45,
                }}
              >
                {t.queue.modeUnified}
              </button>
            </Tooltip>
            <Tooltip mouseEnterDelay={2} title={t.queue.layeredTip}>
              <button
                type="button"
                onClick={() => handleSwitchStyle('layered')}
                style={{
                  padding: '2px 7px',
                  borderRadius: 5,
                  border: 'none',
                  fontSize: 11,
                  fontWeight: progressStyle === 'layered' ? 600 : 400,
                  cursor: 'pointer',
                  background: progressStyle === 'layered' ? token.colorPrimary : 'transparent',
                  color: progressStyle === 'layered' ? '#ffffff' : token.colorTextSecondary,
                  lineHeight: 1.45,
                }}
              >
                {t.queue.modeLayered}
              </button>
            </Tooltip>
          </div>

          {hasFinished && (
            <button
              type="button"
              onClick={clearFinishedQueueJobs}
              style={{
                padding: '2px 6px',
                border: 'none',
                background: 'transparent',
                color: token.colorTextSecondary,
                fontSize: 11,
                cursor: 'pointer',
              }}
            >
              {t.queue.clearFinished}
            </button>
          )}

          <button
            type="button"
            onClick={handleCollapse}
            style={{
              padding: '2px 6px',
              borderRadius: 6,
              border: 'none',
              background: token.colorFillQuaternary,
              color: token.colorTextSecondary,
              fontSize: 11,
              cursor: 'pointer',
            }}
          >
            {t.queue.collapseBtn}
          </button>
        </div>
      </div>

      {/* 任务行列表 */}
      <div
        style={{
          display: 'flex',
          flexDirection: 'column',
          gap: 8,
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
            <div
              key={job.id}
              style={{
                padding: '9px 11px',
                borderRadius: 10,
                background: token.colorFillQuaternary,
                border:
                  job.status === 'failed'
                    ? '1px solid rgba(239, 68, 68, 0.32)'
                    : `1px solid ${token.colorBorderSecondary}`,
                display: 'flex',
                flexDirection: 'column',
                gap: 6,
              }}
            >
              {/* 第一行：应用名 + 版本号变化 + 右侧独立「详情」按钮 */}
              <div
                style={{
                  display: 'flex',
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
                    flexWrap: isCompactWidth ? 'nowrap' : 'wrap',
                  }}
                >
                  <span
                    title={`${displayName} (${job.hostName})`}
                    style={{
                      fontWeight: 600,
                      fontSize: 12.5,
                      color: token.colorText,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {displayName}
                  </span>
                  {(oldVer || tgtVer) && (
                    <span
                      style={{
                        fontFamily: 'monospace',
                        fontSize: 11,
                        color: token.colorTextSecondary,
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                        flexShrink: isCompactWidth ? 1 : 0,
                      }}
                    >
                      {oldVer ? `${oldVer} → ${tgtVer}` : tgtVer}
                    </span>
                  )}
                </div>

                <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0 }}>
                  <button
                    type="button"
                    onClick={() => setActiveLogJobId(job.id)}
                    style={{
                      padding: '2px 8px',
                      borderRadius: 6,
                      border: `1px solid ${token.colorBorder}`,
                      background: token.colorBgContainer,
                      color: token.colorPrimary,
                      fontSize: 11.5,
                      fontWeight: 500,
                      cursor: 'pointer',
                      lineHeight: 1.4,
                    }}
                  >
                    {t.queue.detailBtn}
                  </button>
                  {isFinished && (
                    <button
                      type="button"
                      title={t.queue.dismissTip}
                      onClick={() => dismissQueueJob(job.id)}
                      style={{
                        padding: '2px 5px',
                        border: 'none',
                        background: 'transparent',
                        color: token.colorTextSecondary,
                        fontSize: 11,
                        cursor: 'pointer',
                        lineHeight: 1,
                      }}
                    >
                      ✕
                    </button>
                  )}
                </div>
              </div>

              {/* 第二行：阶段文案 / 字节进度 / 失败摘要 */}
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 8,
                  fontSize: 11,
                }}
              >
                <span
                  title={phaseText}
                  style={{
                    color:
                      job.status === 'failed'
                        ? '#ef4444'
                        : job.status === 'succeeded'
                          ? '#10b981'
                          : token.colorTextSecondary,
                    fontWeight: job.status === 'succeeded' || job.status === 'failed' ? 500 : 400,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    minWidth: 0,
                    flex: 1,
                  }}
                >
                  {phaseText}
                </span>
                {hasBytes && (
                  <span
                    style={{
                      fontFamily: 'monospace',
                      color: token.colorTextSecondary,
                      flexShrink: 0,
                    }}
                  >
                    {formatBytes(job.progress.downloadedBytes)} /{' '}
                    {formatBytes(job.progress.totalBytes)}
                  </span>
                )}
              </div>

              {/* 第三行：分层细条（默认）或 整体单条 */}
              {showLayered ? (
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <div
                    style={{
                      flex: 1,
                      display: 'flex',
                      alignItems: 'center',
                      gap: 3,
                      minWidth: 0,
                    }}
                  >
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
                          ? '#ef4444'
                          : isDone
                            ? '#10b981'
                            : '#2563eb'
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
                              borderRadius: 3,
                              background: token.colorFillSecondary,
                              overflow: 'hidden',
                            }}
                          >
                            <div
                              style={{
                                width: `${pct}%`,
                                height: '100%',
                                borderRadius: 3,
                                background: fillBg,
                                transition: 'width 0.22s ease, background-color 0.2s ease',
                              }}
                            />
                          </div>
                        </Tooltip>
                      )
                    })}
                  </div>
                  <span
                    style={{
                      fontFamily: 'monospace',
                      fontSize: 11,
                      color: token.colorTextSecondary,
                      flexShrink: 0,
                    }}
                  >
                    {doneLayers}/{layers.length}
                  </span>
                </div>
              ) : (
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <div
                    style={{
                      flex: 1,
                      height: 8,
                      borderRadius: 4,
                      background: token.colorFillSecondary,
                      overflow: 'hidden',
                    }}
                  >
                    <div
                      style={{
                        width: `${unifiedPct}%`,
                        height: '100%',
                        borderRadius: 4,
                        background:
                          job.status === 'failed'
                            ? '#ef4444'
                            : job.status === 'succeeded'
                              ? '#10b981'
                              : '#2563eb',
                        transition: 'width 0.25s ease, background-color 0.2s ease',
                      }}
                    />
                  </div>
                  <span
                    style={{
                      fontFamily: 'monospace',
                      fontSize: 11,
                      color: token.colorTextSecondary,
                      flexShrink: 0,
                    }}
                  >
                    {Math.round(unifiedPct)}%
                  </span>
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
