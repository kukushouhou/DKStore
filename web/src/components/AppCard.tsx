import { useMemo, useState } from 'react'
import { Alert, Modal, Progress, Space, Typography, message, theme } from 'antd'
import {
  api,
  resolveIconUrl,
  type AppCardItem,
  type ServiceItem,
  type UpdateJobItem,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import DetailDrawer from './DetailDrawer'
import IgnorePolicyModal from './IgnorePolicyModal'
import ProjectUpdateModal from './ProjectUpdateModal'
import VersionPickerModal from './VersionPickerModal'

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

const avatarColors = ['#2563eb', '#0d9488', '#7c3aed', '#e11d48', '#d97706', '#059669']

function pickColor(name: string): string {
  let h = 0
  for (let i = 0; i < name.length; i++) {
    h = (h * 31 + name.charCodeAt(i)) | 0
  }
  return avatarColors[Math.abs(h) % avatarColors.length]
}

function getInitials(name: string): string {
  const cleaned = name.replace(/[^a-zA-Z0-9\u4e00-\u9fa5]/g, '')
  return (cleaned.slice(0, 2) || name.slice(0, 2) || 'AP').toUpperCase()
}

interface AppCardProps {
  app: AppCardItem
  isChecking?: boolean
  onChanged: () => void
}

export default function AppCard({ app, isChecking = false, onChanged }: AppCardProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()

  const activeUpdateJobs = useUIStore((s) => s.activeUpdateJobs)
  const lastFinishedJobs = useUIStore((s) => s.lastFinishedJobs)
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)

  const iconSrc = resolveIconUrl(app.iconUrl)
  const [failedIconSrc, setFailedIconSrc] = useState<string | null>(null)
  const imgErr = Boolean(iconSrc && failedIconSrc === iconSrc)

  const [pickerSvc, setPickerSvc] = useState<ServiceItem | null>(null)
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [projectInitialTab, setProjectInitialTab] = useState<'direct' | 'yaml'>('direct')
  const [detailOpen, setDetailOpen] = useState(false)
  const [cautionTargetSvc, setCautionTargetSvc] = useState<ServiceItem | null>(null)
  const [updatingSvcKey, setUpdatingSvcKey] = useState('')
  const [restoring, setRestoring] = useState(false)

  const isNoUpdate =
    app.group === 'no_update' || app.group === 'up_to_date' || app.group === 'ignored'
  const isCaution = app.group === 'caution'

  const totalServiceCount =
    app.totalServiceCount ?? app.allServices?.length ?? app.services.length
  const isMultiContainer = totalServiceCount >= 2

  const projectName = app.projectName || (isMultiContainer ? app.appName : undefined)
  const subtitle = isMultiContainer
    ? projectName && projectName !== app.name
      ? `${app.hostName} · ${t.home.projectSubPrefix}${projectName}`
      : `${app.hostName} · ${t.home.servicesCount.replace('{count}', String(totalServiceCount))}`
    : app.hostName

  const primaryService: ServiceItem | null = useMemo(() => {
    const updatable = app.services.find(
      (s) => (s.kind === 'digest_update' || s.kind === 'version_update') && !s.ignored,
    )
    return updatable ?? app.services[0] ?? app.allServices?.[0] ?? null
  }, [app.allServices, app.services])

  const effIgnoreMode = app.ignoreMode ?? primaryService?.ignoreMode
  const isIgnored = Boolean(app.ignored || app.group === 'ignored' || effIgnoreMode || primaryService?.ignored)
  const effIgnorePrefix = app.ignorePrefix ?? primaryService?.ignorePrefix ?? ''
  const effIgnoreTargetTag =
    app.ignoreTargetTag ??
    primaryService?.ignoreTargetTag ??
    app.summaryTarget ??
    primaryService?.targetVersion ??
    ''

  const activeJobsForApp: UpdateJobItem[] = app.services
    .map((s) => activeUpdateJobs[s.key])
    .filter((j): j is UpdateJobItem => Boolean(j))

  const failedJobsForApp: UpdateJobItem[] = app.services
    .map((s) => lastFinishedJobs[s.key])
    .filter((j): j is UpdateJobItem => Boolean(j && j.status === 'failed'))

  const formatPhaseText = (job: UpdateJobItem): string => {
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
        return job.progress?.statusText || job.phase
    }
  }

  const triggerSingleServiceUpdate = async (svc: ServiceItem) => {
    setUpdatingSvcKey(svc.key)
    try {
      const res = await api.startUpdates([svc.key])
      if (res.items && res.items[0]) {
        upsertUpdateJob(res.items[0])
      }
      message.info(t.home.updating)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setUpdatingSvcKey('')
      setCautionTargetSvc(null)
    }
  }

  const handleUpdateClick = () => {
    if (isMultiContainer) {
      setProjectInitialTab('direct')
      setProjectModalOpen(true)
      return
    }
    if (!primaryService) {
      setDetailOpen(true)
      return
    }
    if (isCaution || app.isDatabase || primaryService.category === 'caution') {
      setCautionTargetSvc(primaryService)
      return
    }
    triggerSingleServiceUpdate(primaryService)
  }

  const handleRestoreReminder = async () => {
    setRestoring(true)
    try {
      const igSvcs = app.services.filter((s) => s.ignored || Boolean(s.ignoreMode))
      if (igSvcs.length > 0 && isMultiContainer) {
        await Promise.all(igSvcs.map((s) => api.removeIgnoreByService(s.key)))
      } else if (app.services.length === 1 && isMultiContainer) {
        await api.removeIgnoreByService(app.services[0].key)
      } else {
        await api.removeIgnoreByApp(app.key)
      }
      message.success(t.ignoreModal.restored)
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setRestoring(false)
    }
  }

  // 角色标签（谨慎分组：数据库 / 消息队列 / 中间件）
  const renderRoleBadge = () => {
    if (!isCaution) return null
    const rk = app.roleKind || primaryService?.roleKind
    let text = ''
    if (rk === 'mq') {
      text = t.home.roleMq
    } else if (rk === 'middleware') {
      text = t.home.roleMiddleware
    } else if (app.isDatabase || rk === 'database' || primaryService?.isDatabase) {
      text = t.home.roleDatabase
    }
    if (!text) return null
    return (
      <span
        style={{
          padding: '1px 6px',
          borderRadius: 5,
          background: 'rgba(245, 158, 11, 0.15)',
          color: '#d97706',
          fontSize: 11,
          fontWeight: 500,
          flexShrink: 0,
        }}
      >
        {text}
      </span>
    )
  }

  // 无需更新卡片上的忽略/锁定标签
  const renderIgnoredBadge = () => {
    if (!isNoUpdate || !isIgnored) return null
    if (effIgnoreMode === 'prefix' && effIgnorePrefix) {
      return (
        <span
          style={{
            padding: '1px 6px',
            borderRadius: 5,
            background: 'rgba(47, 107, 255, 0.14)',
            color: token.colorPrimary,
            fontSize: 11,
            fontWeight: 500,
            flexShrink: 0,
          }}
        >
          {t.home.badgeLockedPrefix.replace('{prefix}', effIgnorePrefix)}
        </span>
      )
    }
    if (effIgnoreMode === 'this_version') {
      const label =
        effIgnoreTargetTag && !effIgnoreTargetTag.includes(' ')
          ? t.home.badgeSkippedTag.replace('{tag}', effIgnoreTargetTag)
          : t.home.badgeSkippedGeneric
      return (
        <span
          style={{
            padding: '1px 6px',
            borderRadius: 5,
            background: 'rgba(245, 158, 11, 0.15)',
            color: '#d97706',
            fontSize: 11,
            fontWeight: 500,
            flexShrink: 0,
          }}
        >
          {label}
        </span>
      )
    }
    return (
      <span
        style={{
          padding: '1px 6px',
          borderRadius: 5,
          background: token.colorFillSecondary,
          color: token.colorTextSecondary,
          fontSize: 11,
          fontWeight: 500,
          flexShrink: 0,
        }}
      >
        {t.home.badgeMuted}
      </span>
    )
  }

  const curVer =
    app.summaryCurrent || primaryService?.currentVersion || t.home.unknownCurrent
  const tgtVer =
    app.summaryTarget || primaryService?.targetVersion || t.home.newImageDetected
  const releaseUrl = app.links.releases || app.links.repo
  const ignoreKeys = app.updateServiceKeys?.length
    ? app.updateServiceKeys
    : app.services.map((s) => s.key)

  const updatingNow = activeJobsForApp.length > 0 || Boolean(updatingSvcKey)

  return (
    <>
      <div
        style={{
          height: '100%',
          boxSizing: 'border-box',
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'space-between',
          borderRadius: 14,
          padding: 16,
          background: token.colorBgContainer,
          border:
            isCaution && !isChecking
              ? '1px solid rgba(245, 158, 11, 0.38)'
              : `1px solid ${token.colorBorderSecondary}`,
          transition: 'border-color 0.18s ease, box-shadow 0.18s ease',
        }}
      >
        {/* 上半区：图标名称副标题 + 单行版本状态 */}
        <div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            {iconSrc && !imgErr ? (
              <div
                style={{
                  width: 40,
                  height: 40,
                  borderRadius: 12,
                  background: '#ffffff',
                  border: `1px solid ${token.colorBorderSecondary}`,
                  padding: 6,
                  boxSizing: 'border-box',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  flexShrink: 0,
                }}
              >
                <img
                  src={iconSrc}
                  alt={app.name}
                  onError={() => setFailedIconSrc(iconSrc)}
                  style={{ width: '100%', height: '100%', objectFit: 'contain' }}
                />
              </div>
            ) : (
              <div
                style={{
                  width: 40,
                  height: 40,
                  borderRadius: 12,
                  background: pickColor(app.name),
                  color: '#ffffff',
                  fontWeight: 600,
                  fontSize: 14,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  flexShrink: 0,
                  opacity: isChecking ? 0.85 : 1,
                }}
              >
                {getInitials(app.name)}
              </div>
            )}

            <div style={{ minWidth: 0, flex: 1 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                <span
                  title={app.name}
                  style={{
                    fontWeight: 600,
                    fontSize: 14,
                    color: token.colorText,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {app.name}
                </span>
                {!isChecking && renderRoleBadge()}
                {!isChecking && renderIgnoredBadge()}
                {app.hostStatus === 'offline' && (
                  <span
                    style={{
                      padding: '1px 6px',
                      borderRadius: 5,
                      background: 'rgba(239, 68, 68, 0.14)',
                      color: '#ef4444',
                      fontSize: 11,
                      flexShrink: 0,
                    }}
                  >
                    {t.home.tagOffline}
                  </span>
                )}
              </div>

              <div
                title={subtitle}
                style={{
                  fontSize: 12,
                  color: token.colorTextSecondary,
                  marginTop: 2,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {subtitle}
              </div>
            </div>
          </div>

          {/* 中部单行版本区 */}
          <div style={{ marginTop: 14 }}>
            {isChecking ? (
              <div
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '5px 12px',
                  borderRadius: 8,
                  background: token.colorFillQuaternary,
                  border: `1px solid ${token.colorBorderSecondary}`,
                  fontSize: 12,
                  color: token.colorPrimary,
                }}
              >
                <svg
                  className="dkstore-spin"
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  style={{ flexShrink: 0 }}
                >
                  <circle
                    cx="12"
                    cy="12"
                    r="9"
                    stroke="currentColor"
                    strokeOpacity="0.25"
                    strokeWidth="3"
                  />
                  <path
                    d="M21 12a9 9 0 00-9-9"
                    stroke="currentColor"
                    strokeWidth="3"
                    strokeLinecap="round"
                  />
                </svg>
                <span>{t.home.checkingCard}</span>
              </div>
            ) : isNoUpdate ? (
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 8,
                  fontSize: 12,
                }}
              >
                <span
                  style={{
                    padding: '4px 10px',
                    borderRadius: 8,
                    background: token.colorFillQuaternary,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    fontFamily: 'monospace',
                    color: token.colorTextSecondary,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {curVer}
                </span>

                {!isIgnored ? (
                  app.source === 'vendor_managed' ? (
                    <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                      {t.home.tagVendor}
                    </span>
                  ) : primaryService?.kind === 'check_failed' ? (
                    <span style={{ color: '#d97706', fontWeight: 500, flexShrink: 0 }}>
                      {t.home.checkFailedStatus}
                    </span>
                  ) : primaryService?.kind === 'uncomparable' ? (
                    <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                      {t.home.localImageStatus}
                    </span>
                  ) : (
                    <span style={{ color: '#10b981', fontWeight: 500, flexShrink: 0 }}>
                      {t.home.upToDateStatus}
                    </span>
                  )
                ) : effIgnoreMode === 'prefix' && effIgnorePrefix ? (
                  <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                    {t.home.latestInPrefix.replace('{prefix}', effIgnorePrefix)}
                  </span>
                ) : (
                  <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                    {t.home.pausedReminderStatus}
                  </span>
                )}
              </div>
            ) : (
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  fontSize: 12,
                  fontFamily: 'monospace',
                  flexWrap: 'wrap',
                }}
              >
                <span
                  style={{
                    padding: '4px 10px',
                    borderRadius: 8,
                    background: token.colorFillQuaternary,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    color: token.colorTextSecondary,
                  }}
                >
                  {curVer}
                </span>

                <span style={{ color: token.colorTextSecondary }}>→</span>

                <button
                  type="button"
                  title={t.home.pickOtherVersionTip}
                  className="dkstore-ver-pill"
                  onClick={() => {
                    if (primaryService) setPickerSvc(primaryService)
                  }}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 5,
                    padding: '4px 10px',
                    borderRadius: 8,
                    fontFamily: 'monospace',
                    fontSize: 12,
                    fontWeight: 600,
                    ...(isCaution
                      ? {
                          background: 'rgba(245, 158, 11, 0.12)',
                          border: '1px solid rgba(245, 158, 11, 0.38)',
                          color: '#d97706',
                        }
                      : {
                          background: 'rgba(47, 107, 255, 0.10)',
                          border: '1px solid rgba(47, 107, 255, 0.32)',
                          color: token.colorPrimary,
                        }),
                  }}
                >
                  <span>{tgtVer}</span>
                  <svg
                    width="12"
                    height="12"
                    viewBox="0 0 20 20"
                    fill="currentColor"
                    style={{ opacity: 0.72 }}
                  >
                    <path
                      fillRule="evenodd"
                      d="M5.23 7.21a.75.75 0 011.06.02L10 11.168l3.71-3.938a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
                      clipRule="evenodd"
                    />
                  </svg>
                </button>
              </div>
            )}
          </div>

          {/* 活跃升级进度条 */}
          {activeJobsForApp.map((job) => (
            <div key={job.id} style={{ marginTop: 10 }}>
              <div
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  alignItems: 'center',
                  fontSize: 11,
                  color: token.colorTextSecondary,
                }}
              >
                <span>{formatPhaseText(job)}</span>
                {(job.progress?.totalLayers ?? 0) > 0 && (
                  <span>
                    {t.home.layerStat
                      .replace('{done}', String(job.progress?.completedLayers ?? 0))
                      .replace('{total}', String(job.progress?.totalLayers ?? 0))}
                    {(job.progress?.totalBytes ?? 0) > 0
                      ? ` (${formatBytes(job.progress?.downloadedBytes ?? 0)} / ${formatBytes(job.progress?.totalBytes ?? 0)})`
                      : ''}
                  </span>
                )}
              </div>
              <Progress percent={job.progress?.percent ?? 0} size="small" status="active" />
            </div>
          ))}

          {failedJobsForApp.map((job) => (
            <Alert
              key={job.id}
              type="error"
              showIcon
              title={
                <span style={{ fontSize: 12 }}>
                  {t.home.updateFailed}
                  {job.errorMsg || job.errorCode}
                </span>
              }
              style={{ marginTop: 10, padding: '5px 10px' }}
            />
          ))}
        </div>

        {/* 底部对齐操作栏 */}
        <div
          style={{
            marginTop: 14,
            paddingTop: 12,
            borderTop: `1px solid ${token.colorBorderSecondary}`,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 8,
            fontSize: 12,
            opacity: isChecking ? 0.45 : 1,
            pointerEvents: isChecking ? 'none' : 'auto',
          }}
        >
          {/* 底栏左侧：更新说明/官方仓库 · 详情 */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
            {releaseUrl && (
              <>
                <a
                  href={releaseUrl}
                  target="_blank"
                  rel="noreferrer"
                  style={{
                    color: token.colorPrimary,
                    textDecoration: 'none',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {app.links.releases ? t.home.viewReleaseNotes : t.home.viewRepo}
                </a>
                <span style={{ color: token.colorBorder }}>·</span>
              </>
            )}
            <button
              type="button"
              onClick={() => setDetailOpen(true)}
              style={{
                border: 'none',
                background: 'transparent',
                color: token.colorTextSecondary,
                padding: 0,
                fontSize: 12,
                cursor: 'pointer',
                whiteSpace: 'nowrap',
              }}
            >
              {t.home.detailsBtn}
            </button>
          </div>

          {/* 底栏右侧：按分组渲染操作按钮 */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
            {isNoUpdate ? (
              <>
                {app.source !== 'vendor_managed' &&
                  primaryService &&
                  primaryService.kind !== 'uncomparable' && (
                  <button
                    type="button"
                    onClick={() => setPickerSvc(primaryService)}
                    style={{
                      padding: '4px 10px',
                      borderRadius: 8,
                      border: `1px solid ${token.colorBorder}`,
                      background: token.colorFillQuaternary,
                      color: token.colorText,
                      fontSize: 12,
                      cursor: 'pointer',
                    }}
                  >
                    {t.home.switchVersion}
                  </button>
                )}
                {isIgnored && (
                  <button
                    type="button"
                    disabled={restoring}
                    onClick={handleRestoreReminder}
                    style={{
                      padding: '4px 10px',
                      borderRadius: 8,
                      border: '1px solid rgba(47, 107, 255, 0.35)',
                      background: 'rgba(47, 107, 255, 0.10)',
                      color: token.colorPrimary,
                      fontSize: 12,
                      fontWeight: 500,
                      cursor: 'pointer',
                    }}
                  >
                    {t.home.restoreReminder}
                  </button>
                )}
              </>
            ) : (
              <>
                <button
                  type="button"
                  onClick={() => setIgnoreOpen(true)}
                  style={{
                    padding: '4px 10px',
                    borderRadius: 8,
                    border: `1px solid ${token.colorBorder}`,
                    background: token.colorFillQuaternary,
                    color: token.colorText,
                    fontSize: 12,
                    cursor: 'pointer',
                  }}
                >
                  {t.home.ignore}
                </button>

                {app.source === 'vendor_managed' ? (
                  <span
                    title={t.home.vendorReadonlyTip}
                    style={{
                      padding: '4px 8px',
                      borderRadius: 8,
                      background: token.colorFillSecondary,
                      color: token.colorTextSecondary,
                      fontSize: 11,
                    }}
                  >
                    {t.home.tagVendor}
                  </span>
                ) : (
                  <button
                    type="button"
                    disabled={updatingNow}
                    onClick={handleUpdateClick}
                    style={{
                      padding: '4px 12px',
                      borderRadius: 8,
                      border: 'none',
                      background: isCaution ? '#f59e0b' : token.colorPrimary,
                      color: '#ffffff',
                      fontSize: 12,
                      fontWeight: 500,
                      cursor: updatingNow ? 'not-allowed' : 'pointer',
                      opacity: updatingNow ? 0.75 : 1,
                    }}
                  >
                    {updatingNow ? t.home.updating : t.home.updateBtn}
                  </button>
                )}
              </>
            )}
          </div>
        </div>
      </div>

      {/* 单容器谨慎更新确认弹窗 */}
      <Modal
        open={Boolean(cautionTargetSvc)}
        title={t.home.cautionConfirmTitle}
        okText={t.home.cautionConfirmBtn}
        cancelText={t.ignoreModal.cancel}
        okButtonProps={{ danger: true, loading: Boolean(updatingSvcKey) }}
        onOk={() => cautionTargetSvc && triggerSingleServiceUpdate(cautionTargetSvc)}
        onCancel={() => setCautionTargetSvc(null)}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Alert
            type="warning"
            showIcon
            title={cautionTargetSvc?.reasonText || app.reasonText || t.home.cautionConfirmTitle}
          />
          <Typography.Text>{t.home.cautionConfirmDesc}</Typography.Text>
        </Space>
      </Modal>

      {/* 双栏版本选择弹窗 */}
      <VersionPickerModal
        open={Boolean(pickerSvc)}
        appName={pickerSvc?.name || app.name}
        subtitle={subtitle}
        service={pickerSvc}
        onClose={() => setPickerSvc(null)}
        onUpdated={onChanged}
      />

      {/* 忽略策略设置弹窗 */}
      <IgnorePolicyModal
        open={ignoreOpen}
        name={app.name}
        subtitle={subtitle}
        serviceKeys={ignoreKeys}
        currentVersion={curVer !== t.home.unknownCurrent ? curVer : undefined}
        targetVersion={tgtVer !== t.home.newImageDetected ? tgtVer : undefined}
        targetDigest={primaryService?.targetDigest}
        onClose={() => setIgnoreOpen(false)}
        onSuccess={onChanged}
      />

      {/* 多容器项目协同更新 / 按官方新版配置更新弹窗 */}
      <ProjectUpdateModal
        open={projectModalOpen}
        app={app}
        initialTab={projectInitialTab}
        onClose={() => setProjectModalOpen(false)}
        onUpdated={onChanged}
      />

      {/* 次要信息收纳抽屉 */}
      <DetailDrawer
        open={detailOpen}
        app={app}
        onClose={() => setDetailOpen(false)}
        onOpenVersionPicker={(svc) => {
          setDetailOpen(false)
          setPickerSvc(svc)
        }}
        onOpenComposeYaml={() => {
          setDetailOpen(false)
          setProjectInitialTab('yaml')
          setProjectModalOpen(true)
        }}
      />
    </>
  )
}
