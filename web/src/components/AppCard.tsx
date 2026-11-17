import { useCallback, useMemo, useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfigProvider,
  Dropdown,
  Flex,
  Modal,
  Popconfirm,
  Progress,
  Space,
  Tag,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  ArrowRightOutlined,
  CloseCircleFilled,
  DownOutlined,
  EditOutlined,
  ExportOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import {
  api,
  showApiError,
  type AppCardItem,
  type LifecycleAction,
  type ServiceItem,
  type UpdateJobItem,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import AppIconAvatar from './AppIconAvatar'
import ContainerMetaModal from './ContainerMetaModal'
import DeleteAppModal from './DeleteAppModal'
import DetailDrawer from './DetailDrawer'
import IgnorePolicyModal from './IgnorePolicyModal'
import {
  buildMetaContextFromCard,
  getHomeCardDisplayInfo,
  pickUserTagColor,
  resolveHomeCardUrl,
} from './metaUtils'
import ProjectUpdateModal from './ProjectUpdateModal'
import StoppedUpdateModal from './StoppedUpdateModal'
import VersionPickerModal from './VersionPickerModal'
import { unwrapConcreteVersion } from './versionUtils'

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

interface AppCardProps {
  app: AppCardItem
  isChecking?: boolean
  allTags?: string[]
  activeTag?: string
  onTagClick?: (tag: string) => void
  onChanged: () => void
}

export default function AppCard({
  app,
  isChecking = false,
  allTags = [],
  activeTag = '',
  onTagClick,
  onChanged,
}: AppCardProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const qc = useQueryClient()

  const activeUpdateJobs = useUIStore((s) => s.activeUpdateJobs)
  const lastFinishedJobs = useUIStore((s) => s.lastFinishedJobs)
  const updateQueueJobs = useUIStore((s) => s.updateQueueJobs)
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)
  const dismissQueueJob = useUIStore((s) => s.dismissQueueJob)
  const setActiveLogJobId = useUIStore((s) => s.setActiveLogJobId)

  const [metaOpen, setMetaOpen] = useState(false)
  const [pickerSvc, setPickerSvc] = useState<ServiceItem | null>(null)
  const [pickerInitialTab, setPickerInitialTab] = useState<'version' | 'config'>('version')
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [projectInitialTab, setProjectInitialTab] = useState<'direct' | 'yaml'>('direct')
  const [detailOpen, setDetailOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [cautionTargetSvc, setCautionTargetSvc] = useState<ServiceItem | null>(null)
  const [stoppedUpdateSvc, setStoppedUpdateSvc] = useState<ServiceItem | null>(null)
  const [stoppedLoadingChoice, setStoppedLoadingChoice] = useState<'keep' | 'start' | null>(null)
  const [pendingLifecycleAction, setPendingLifecycleAction] = useState<LifecycleAction | null>(null)
  const [lifecycleSubmitting, setLifecycleSubmitting] = useState(false)
  const [updatingSvcKey, setUpdatingSvcKey] = useState('')
  const [retryingJobId, setRetryingJobId] = useState<string | null>(null)
  const [expandedErrIds, setExpandedErrIds] = useState<Record<string, boolean>>({})
  const [restoring, setRestoring] = useState(false)

  const isNoUpdate =
    app.group === 'no_update' || app.group === 'up_to_date' || app.group === 'ignored'
  const isCaution = app.group === 'caution'
  const cautionBtnTheme = useMemo(
    () =>
      isCaution
        ? {
            token: { colorPrimary: token.colorWarning },
            components: { Button: { primaryShadow: 'none' } },
          }
        : undefined,
    [isCaution, token.colorWarning],
  )

  const totalServiceCount =
    app.totalServiceCount ?? app.allServices?.length ?? app.services.length
  const isMultiContainer = totalServiceCount >= 2
  const isProjectApp =
    app.source === 'compose' || app.source === 'dsm_project' || isMultiContainer

  const projectName = app.projectName || (isMultiContainer ? app.appName : undefined)
  const { title: displayTitle, subtitle, tags: userTags } = useMemo(
    () => getHomeCardDisplayInfo(app, t.home.projectSubPrefix, t.home.servicesCount),
    [app, t.home.projectSubPrefix, t.home.servicesCount],
  )
  const accessUrl = useMemo(() => resolveHomeCardUrl(app), [app])
  const metaCtx = useMemo(
    () => (metaOpen ? buildMetaContextFromCard(app, t.metaModal.wholeProject) : null),
    [app, metaOpen, t.metaModal.wholeProject],
  )

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

  const activeJobsForApp: UpdateJobItem[] = useMemo(() => {
    const seen = new Set<string>()
    const list: UpdateJobItem[] = []
    for (const s of app.services) {
      const j = activeUpdateJobs[s.key]
      if (j && !seen.has(j.id)) {
        seen.add(j.id)
        list.push(j)
      }
    }
    for (const j of Object.values(updateQueueJobs)) {
      if (
        j.appKey === app.key &&
        (j.status === 'queued' || j.status === 'running') &&
        !seen.has(j.id)
      ) {
        seen.add(j.id)
        list.push(j)
      }
    }
    return list
  }, [activeUpdateJobs, app.key, app.services, updateQueueJobs])

  const failedJobsForApp: UpdateJobItem[] = useMemo(() => {
    const seen = new Set<string>()
    const list: UpdateJobItem[] = []
    for (const s of app.services) {
      const j = lastFinishedJobs[s.key]
      if (j && j.status === 'failed' && !seen.has(j.id)) {
        seen.add(j.id)
        list.push(j)
      }
    }
    for (const j of Object.values(updateQueueJobs)) {
      if (j.appKey === app.key && j.status === 'failed' && !seen.has(j.id)) {
        seen.add(j.id)
        list.push(j)
      }
    }
    return list
  }, [app.key, app.services, lastFinishedJobs, updateQueueJobs])

  const formatPhaseText = (job: UpdateJobItem): string => {
    switch (job.phase || job.progress?.phase) {
      case 'precheck':
        return t.home.phasePrecheck
      case 'pulling':
        return (job.progress?.totalLayers ?? 0) === 0 && (job.progress?.downloadedBytes ?? 0) === 0
          ? t.home.phaseFetchingLayers
          : t.home.phasePulling
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

  const isSvcNotRunning = (svc: ServiceItem | null | undefined): boolean =>
    Boolean(svc?.state && svc.state.trim() && svc.state !== 'running')

  const triggerSingleServiceUpdate = async (svc: ServiceItem, startAfter?: boolean) => {
    setUpdatingSvcKey(svc.key)
    if (startAfter !== undefined) {
      setStoppedLoadingChoice(startAfter ? 'start' : 'keep')
    }
    try {
      const res =
        startAfter !== undefined
          ? await api.startUpdates({ items: [{ serviceKey: svc.key, startAfter }] })
          : await api.startUpdates([svc.key])
      if (res.items && res.items[0]) {
        upsertUpdateJob(res.items[0])
      }
      message.info(t.home.updating)
      setStoppedUpdateSvc(null)
    } catch (e) {
      showApiError(e)
    } finally {
      setUpdatingSvcKey('')
      setStoppedLoadingChoice(null)
      setCautionTargetSvc(null)
    }
  }

  const requestSingleServiceUpdate = (svc: ServiceItem) => {
    if (isSvcNotRunning(svc)) {
      setCautionTargetSvc(null)
      setStoppedUpdateSvc(svc)
      return
    }
    void triggerSingleServiceUpdate(svc)
  }

  const handleRetryFailedJob = async (jobId: string) => {
    setRetryingJobId(jobId)
    try {
      const next = await api.retryUpdateJob(jobId)
      dismissQueueJob(jobId)
      upsertUpdateJob(next)
      message.info(t.home.updating)
    } catch (e) {
      showApiError(e)
    } finally {
      setRetryingJobId(null)
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
    requestSingleServiceUpdate(primaryService)
  }

  const handleUpdateByConfigClick = useCallback(() => {
    if (isMultiContainer) {
      setProjectInitialTab('yaml')
      setProjectModalOpen(true)
      return
    }
    if (primaryService) {
      setPickerInitialTab('config')
      setPickerSvc(primaryService)
    }
  }, [isMultiContainer, primaryService])

  const cardServicesPool = useMemo(
    () => (app.allServices?.length ? app.allServices : app.services),
    [app.allServices, app.services],
  )
  const cardRunning = useMemo(() => {
    const withState = cardServicesPool.filter((s) => Boolean(s.state && s.state.trim()))
    if (withState.length === 0) return true
    return withState.some((s) => s.state === 'running')
  }, [cardServicesPool])

  const handleConfirmLifecycle = async () => {
    if (!pendingLifecycleAction) return
    const action = pendingLifecycleAction
    setLifecycleSubmitting(true)
    try {
      await api.controlAppLifecycle({
        appKey: app.key,
        action,
      })
      const msgTpl = isMultiContainer
        ? action === 'start'
          ? t.lifecycle.startedProjectMsg
          : action === 'stop'
            ? t.lifecycle.stoppedProjectMsg
            : t.lifecycle.restartedProjectMsg
        : action === 'start'
          ? t.lifecycle.startedMsg
          : action === 'stop'
            ? t.lifecycle.stoppedMsg
            : t.lifecycle.restartedMsg
      message.success(msgTpl.replace('{name}', displayTitle))
      setPendingLifecycleAction(null)
      void qc.invalidateQueries({ queryKey: ['containers'] })
      void qc.invalidateQueries({ queryKey: ['containers-fast'] })
      void qc.invalidateQueries({ queryKey: ['containers-cached'] })
      onChanged()
    } catch (e) {
      showApiError(e)
    } finally {
      setLifecycleSubmitting(false)
    }
  }

  const lifecyclePopTitle = useMemo(() => {
    if (!pendingLifecycleAction) return ''
    const tpl = isMultiContainer
      ? pendingLifecycleAction === 'start'
        ? t.lifecycle.confirmStartProject
        : pendingLifecycleAction === 'stop'
          ? t.lifecycle.confirmStopProject
          : t.lifecycle.confirmRestartProject
      : pendingLifecycleAction === 'start'
        ? t.lifecycle.confirmStart
        : pendingLifecycleAction === 'stop'
          ? t.lifecycle.confirmStop
          : t.lifecycle.confirmRestart
    return tpl.replace('{name}', displayTitle)
  }, [displayTitle, isMultiContainer, pendingLifecycleAction, t.lifecycle])

  const deleteMenuLabel = isProjectApp ? t.home.deleteProjectMenu : t.home.deleteContainerMenu
  const canEditConfig = isMultiContainer || Boolean(primaryService)
  const extendedMenuItems = useMemo(
    () => [
      ...(cardRunning
        ? [
            {
              key: 'restart',
              label: isMultiContainer ? t.lifecycle.restartProject : t.lifecycle.restartContainer,
              onClick: () => setPendingLifecycleAction('restart'),
            },
            {
              key: 'stop',
              label: isMultiContainer ? t.lifecycle.stopProject : t.lifecycle.stopContainer,
              onClick: () => setPendingLifecycleAction('stop'),
            },
          ]
        : [
            {
              key: 'start',
              label: isMultiContainer ? t.lifecycle.startProject : t.lifecycle.startContainer,
              onClick: () => setPendingLifecycleAction('start'),
            },
          ]),
      { type: 'divider' as const },
      ...(canEditConfig
        ? [
            {
              key: 'by-config',
              label: t.home.updateByNewConfigMenu,
              onClick: handleUpdateByConfigClick,
            },
            { type: 'divider' as const },
          ]
        : []),
      {
        key: 'delete',
        label: deleteMenuLabel,
        danger: true,
        onClick: () => setDeleteOpen(true),
      },
    ],
    [
      canEditConfig,
      cardRunning,
      deleteMenuLabel,
      handleUpdateByConfigClick,
      isMultiContainer,
      t.home.updateByNewConfigMenu,
      t.lifecycle.restartContainer,
      t.lifecycle.restartProject,
      t.lifecycle.startContainer,
      t.lifecycle.startProject,
      t.lifecycle.stopContainer,
      t.lifecycle.stopProject,
    ],
  )

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
      showApiError(e)
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
      <Tag variant="filled" color="warning" style={{ margin: 0, flexShrink: 0 }}>
        {text}
      </Tag>
    )
  }

  // 无需更新卡片上的忽略/锁定标签
  const renderIgnoredBadge = () => {
    if (!isNoUpdate || !isIgnored) return null
    if (effIgnoreMode === 'prefix' && effIgnorePrefix) {
      return (
        <Tag variant="filled" color="processing" style={{ margin: 0, flexShrink: 0 }}>
          {t.home.badgeLockedPrefix.replace('{prefix}', effIgnorePrefix)}
        </Tag>
      )
    }
    if (effIgnoreMode === 'this_version') {
      const cleanIgnoredTag = unwrapConcreteVersion(effIgnoreTargetTag)
      const label =
        cleanIgnoredTag && !cleanIgnoredTag.includes(' ')
          ? t.home.badgeSkippedTag.replace('{tag}', cleanIgnoredTag)
          : t.home.badgeSkippedGeneric
      return (
        <Tag variant="filled" color="warning" style={{ margin: 0, flexShrink: 0 }}>
          {label}
        </Tag>
      )
    }
    return (
      <Tag variant="filled" style={{ margin: 0, flexShrink: 0 }}>
        {t.home.badgeMuted}
      </Tag>
    )
  }

  // 容器未运行状态标识（图标保持原色，消除“停止更新”歧义）
  const renderNotRunningBadge = () => {
    const cardSvcs = app.services?.length ? app.services : []
    const withState = cardSvcs.filter((s) => Boolean(s.state && s.state.trim()))
    if (withState.length === 0) return null
    const stoppedSvcs = withState.filter((s) => s.state !== 'running')
    if (stoppedSvcs.length === 0) return null

    const allStopped = stoppedSvcs.length === withState.length
    const tagNode = (
      <Tag
        variant="filled"
        style={{
          margin: 0,
          flexShrink: 0,
          display: 'inline-flex',
          alignItems: 'center',
          gap: 5,
        }}
      >
        <Badge status="default" />
        <span>{allStopped ? t.home.tagNotRunning : t.home.tagPartNotRunning}</span>
      </Tag>
    )

    if (!allStopped) {
      return (
        <Tooltip
          title={t.home.notRunningServicesTip.replace(
            '{names}',
            stoppedSvcs.map((s) => s.name).join(', '),
          )}
        >
          {tagNode}
        </Tooltip>
      )
    }
    return tagNode
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
      <Card
        size="small"
        styles={{
          body: {
            padding: '16px 18px',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'space-between',
          },
        }}
        style={{
          borderColor: isCaution && !isChecking ? token.colorWarningBorder : undefined,
        }}
      >
        {/* 上半区：图标名称副标题 + 独立标签行 */}
        <div>
          <Flex align="flex-start" justify="space-between" gap={8}>
            <Flex align="center" gap={12} style={{ minWidth: 0, flex: 1 }}>
              <AppIconAvatar
                name={displayTitle}
                fallbackColorSeed={app.name}
                iconUrl={app.iconUrl}
                size={42}
                dimmed={isChecking}
              />

              <div style={{ minWidth: 0, flex: 1 }}>
                <Flex align="center" gap={4} style={{ minWidth: 0 }}>
                  <Typography.Text
                    strong
                    ellipsis={{ tooltip: displayTitle }}
                    style={{ fontSize: 15 }}
                  >
                    {displayTitle}
                  </Typography.Text>
                  <Tooltip title={t.home.editInfoTip}>
                    <Button
                      type="text"
                      size="small"
                      aria-label={t.home.editInfoTip}
                      icon={<EditOutlined style={{ fontSize: 13 }} />}
                      onClick={() => setMetaOpen(true)}
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
                  {!isChecking && renderNotRunningBadge()}
                  {!isChecking && renderRoleBadge()}
                  {!isChecking && renderIgnoredBadge()}
                  {app.hostStatus === 'offline' && (
                    <Tag variant="filled" color="error" style={{ margin: 0, flexShrink: 0 }}>
                      {t.home.tagOffline}
                    </Tag>
                  )}
                </Flex>

                <Typography.Text
                  type="secondary"
                  ellipsis={{ tooltip: subtitle }}
                  style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
                >
                  {subtitle}
                </Typography.Text>
              </div>
            </Flex>

            {accessUrl && (
              <Tooltip title={t.home.openWebTip.replace('{url}', accessUrl)}>
                <Button
                  type="text"
                  size="small"
                  aria-label={t.home.openWebTip.replace('{url}', accessUrl)}
                  icon={<ExportOutlined />}
                  href={accessUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  style={{
                    color: token.colorTextSecondary,
                    flexShrink: 0,
                    marginRight: -4,
                    marginTop: -2,
                  }}
                />
              </Tooltip>
            )}
          </Flex>

          {userTags.length > 0 && (
            <Flex wrap="wrap" gap={6} style={{ marginTop: 10 }}>
              {userTags.map((tg) => {
                const active = activeTag === tg
                return (
                  <Tag
                    key={tg}
                    variant={active ? 'solid' : 'filled'}
                    color={active ? 'blue' : pickUserTagColor(tg)}
                    onClick={onTagClick ? () => onTagClick(tg) : undefined}
                    style={{
                      margin: 0,
                      cursor: onTagClick ? 'pointer' : 'default',
                      userSelect: 'none',
                    }}
                  >
                    {tg}
                  </Tag>
                )
              })}
            </Flex>
          )}
        </div>

        {/* 下半锚定区：中部版本状态 + 升级进度 + 底部操作栏（确保同排有无标签卡片底部基准线齐平） */}
        <div style={{ marginTop: 14 }}>
          <div>
            {isChecking ? (
              <Tag
                icon={<SyncOutlined spin />}
                color="processing"
                variant="filled"
                style={{ margin: 0, padding: '3px 10px', fontSize: token.fontSizeSM }}
              >
                {t.home.checkingCard}
              </Tag>
            ) : isNoUpdate ? (
              <Flex align="center" justify="space-between" gap={8}>
                <Tag
                  style={{
                    margin: 0,
                    fontFamily: token.fontFamilyCode,
                    fontSize: token.fontSizeSM,
                    padding: '2px 8px',
                    maxWidth: '65%',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {curVer}
                </Tag>

                {!isIgnored ? (
                  app.source === 'vendor_managed' ? (
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                    >
                      {t.home.tagVendor}
                    </Typography.Text>
                  ) : primaryService?.kind === 'check_failed' ? (
                    <Typography.Text
                      type="warning"
                      strong
                      style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                    >
                      {t.home.checkFailedStatus}
                    </Typography.Text>
                  ) : primaryService?.kind === 'uncomparable' ? (
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                    >
                      {t.home.localImageStatus}
                    </Typography.Text>
                  ) : (
                    <Typography.Text
                      type="success"
                      strong
                      style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                    >
                      {t.home.upToDateStatus}
                    </Typography.Text>
                  )
                ) : effIgnoreMode === 'prefix' && effIgnorePrefix ? (
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                  >
                    {t.home.latestInPrefix.replace('{prefix}', effIgnorePrefix)}
                  </Typography.Text>
                ) : (
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                  >
                    {t.home.pausedReminderStatus}
                  </Typography.Text>
                )}
              </Flex>
            ) : (
              <Space size={6} wrap align="center">
                <Tag
                  style={{
                    margin: 0,
                    fontFamily: token.fontFamilyCode,
                    fontSize: token.fontSizeSM,
                    padding: '2px 8px',
                  }}
                >
                  {curVer}
                </Tag>

                <ArrowRightOutlined style={{ fontSize: 11, color: token.colorTextTertiary }} />

                <ConfigProvider theme={cautionBtnTheme}>
                  <Tooltip title={t.home.pickOtherVersionTip}>
                    <Button
                      size="small"
                      type="primary"
                      ghost
                      onClick={() => {
                        if (primaryService) {
                          setPickerInitialTab('version')
                          setPickerSvc(primaryService)
                        }
                      }}
                      style={{
                        fontFamily: token.fontFamilyCode,
                        fontWeight: 600,
                      }}
                    >
                      {tgtVer} <DownOutlined style={{ fontSize: 10 }} />
                    </Button>
                  </Tooltip>
                </ConfigProvider>
              </Space>
            )}
          </div>

          {/* 活跃升级进度条 + 实时查看日志入口 */}
          {activeJobsForApp.map((job) => (
            <div key={job.id} style={{ marginTop: 10 }}>
              <Flex justify="space-between" align="center" gap={6}>
                <Typography.Text
                  type="secondary"
                  ellipsis
                  style={{ fontSize: token.fontSizeSM, flex: 1, minWidth: 0 }}
                >
                  {formatPhaseText(job)}
                </Typography.Text>
                <Space size={6} style={{ flexShrink: 0 }}>
                  {(job.progress?.totalLayers ?? 0) > 0 && (
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {t.home.layerStat
                        .replace('{done}', String(job.progress?.completedLayers ?? 0))
                        .replace('{total}', String(job.progress?.totalLayers ?? 0))}
                      {(job.progress?.totalBytes ?? 0) > 0
                        ? ` (${formatBytes(job.progress?.downloadedBytes ?? 0)} / ${formatBytes(job.progress?.totalBytes ?? 0)})`
                        : ''}
                    </Typography.Text>
                  )}
                  <Typography.Link
                    onClick={() => setActiveLogJobId(job.id)}
                    style={{ fontSize: token.fontSizeSM }}
                  >
                    {t.home.viewLogs}
                  </Typography.Link>
                </Space>
              </Flex>
              <Progress percent={job.progress?.percent ?? 0} size="small" status="active" />
            </div>
          ))}

          {failedJobsForApp.map((job) => {
            const errBody = job.errorMsg || job.errorCode || ''
            const isLongErr = errBody.length > 68
            const isExpanded = Boolean(expandedErrIds[job.id])
            return (
              <div
                key={job.id}
                style={{
                  marginTop: 10,
                  padding: '8px 10px',
                  borderRadius: token.borderRadius,
                  border: `1px solid ${token.colorErrorBorder}`,
                  background: token.colorErrorBg,
                }}
              >
                <div
                  style={{
                    fontSize: token.fontSizeSM,
                    lineHeight: 1.5,
                    color: token.colorText,
                    wordBreak: 'break-word',
                    ...(isExpanded
                      ? {}
                      : {
                          display: '-webkit-box',
                          WebkitLineClamp: 2,
                          WebkitBoxOrient: 'vertical',
                          overflow: 'hidden',
                        }),
                  }}
                >
                  <CloseCircleFilled
                    style={{ color: token.colorError, marginRight: 6, fontSize: 13 }}
                  />
                  <Typography.Text strong style={{ fontSize: token.fontSizeSM, color: token.colorError }}>
                    {t.home.updateFailed}
                  </Typography.Text>
                  {errBody}
                </div>

                <Flex
                  align="center"
                  justify="space-between"
                  gap={8}
                  style={{
                    marginTop: 6,
                    paddingTop: 6,
                    borderTop: `1px solid ${token.colorErrorBorder}`,
                  }}
                >
                  {isLongErr ? (
                    <Typography.Link
                      onClick={() =>
                        setExpandedErrIds((prev) => ({ ...prev, [job.id]: !prev[job.id] }))
                      }
                      style={{ fontSize: 12, color: token.colorTextSecondary }}
                    >
                      {isExpanded ? t.home.collapseError : t.home.expandError}
                    </Typography.Link>
                  ) : (
                    <span />
                  )}

                  <Space size={8} align="center">
                    <Typography.Link
                      onClick={() => setActiveLogJobId(job.id)}
                      style={{ fontSize: token.fontSizeSM, whiteSpace: 'nowrap' }}
                    >
                      {t.home.viewLogs}
                    </Typography.Link>
                    <Button
                      size="small"
                      type="primary"
                      loading={retryingJobId === job.id}
                      onClick={() => handleRetryFailedJob(job.id)}
                    >
                      {t.queue.retryBtn}
                    </Button>
                  </Space>
                </Flex>
              </div>
            )
          })}

          {/* 底部对齐操作栏 */}
        <Flex
          align="center"
          justify="space-between"
          gap={8}
          style={{
            marginTop: 14,
            paddingTop: 12,
            borderTop: `1px solid ${token.colorSplit}`,
            opacity: isChecking ? 0.45 : 1,
            pointerEvents: isChecking ? 'none' : 'auto',
          }}
        >
          {/* 底栏左侧：更新说明/官方仓库 · 详情 */}
          <Space
            size={6}
            align="center"
            separator={<Typography.Text type="secondary">·</Typography.Text>}
            style={{ minWidth: 0 }}
          >
            {releaseUrl && (
              <Typography.Link
                href={releaseUrl}
                target="_blank"
                style={{ fontSize: token.fontSize, whiteSpace: 'nowrap' }}
              >
                {app.links.releases ? t.home.viewReleaseNotes : t.home.viewRepo}
              </Typography.Link>
            )}
            <Typography.Link
              type="secondary"
              onClick={() => setDetailOpen(true)}
              style={{ fontSize: token.fontSize, whiteSpace: 'nowrap' }}
            >
              {t.home.detailsBtn}
            </Typography.Link>
          </Space>

          {/* 底栏右侧：按分组渲染操作按钮 */}
          {isNoUpdate ? (
            <Space size={8} style={{ flexShrink: 0 }}>
              {isIgnored && (
                <Button
                  size="small"
                  type="primary"
                  ghost
                  loading={restoring}
                  onClick={handleRestoreReminder}
                >
                  {t.home.restoreReminder}
                </Button>
              )}
              {app.source !== 'vendor_managed' && (
                <Popconfirm
                  open={Boolean(pendingLifecycleAction)}
                  placement="topRight"
                  title={lifecyclePopTitle}
                  okText={t.lifecycle.ok}
                  cancelText={t.lifecycle.cancel}
                  okButtonProps={{
                    danger: pendingLifecycleAction === 'stop',
                    loading: lifecycleSubmitting,
                  }}
                  onConfirm={handleConfirmLifecycle}
                  onCancel={() => {
                    if (!lifecycleSubmitting) setPendingLifecycleAction(null)
                  }}
                >
                  {primaryService && primaryService.kind !== 'uncomparable' ? (
                    <Space.Compact size="small">
                      <Button
                        disabled={updatingNow || lifecycleSubmitting}
                        onClick={() => {
                          setPickerInitialTab('version')
                          setPickerSvc(primaryService)
                        }}
                      >
                        {t.home.switchVersion}
                      </Button>
                      <Dropdown
                        trigger={['click']}
                        placement="bottomRight"
                        disabled={updatingNow || lifecycleSubmitting}
                        menu={{ items: extendedMenuItems }}
                      >
                        <Button
                          disabled={updatingNow || lifecycleSubmitting}
                          aria-label={t.home.moreActionsAria}
                          icon={<DownOutlined />}
                        />
                      </Dropdown>
                    </Space.Compact>
                  ) : (
                    <Dropdown
                      trigger={['click']}
                      placement="bottomRight"
                      disabled={updatingNow || lifecycleSubmitting}
                      menu={{ items: extendedMenuItems }}
                    >
                      <Button
                        size="small"
                        disabled={updatingNow || lifecycleSubmitting}
                        aria-label={t.home.moreActionsAria}
                        icon={<DownOutlined />}
                      />
                    </Dropdown>
                  )}
                </Popconfirm>
              )}
            </Space>
          ) : (
            <Space size={8} style={{ flexShrink: 0 }}>
              <Button size="small" onClick={() => setIgnoreOpen(true)}>
                {t.home.ignore}
              </Button>

              {app.source === 'vendor_managed' ? (
                <Tooltip title={t.home.vendorReadonlyTip}>
                  <Tag variant="filled" style={{ margin: 0 }}>
                    {t.home.tagVendor}
                  </Tag>
                </Tooltip>
              ) : (
                /* 统一组合主按钮 [ 更新 | ▾ ] */
                <ConfigProvider theme={cautionBtnTheme}>
                  <Popconfirm
                    open={Boolean(pendingLifecycleAction)}
                    placement="topRight"
                    title={lifecyclePopTitle}
                    okText={t.lifecycle.ok}
                    cancelText={t.lifecycle.cancel}
                    okButtonProps={{
                      danger: pendingLifecycleAction === 'stop',
                      loading: lifecycleSubmitting,
                    }}
                    onConfirm={handleConfirmLifecycle}
                    onCancel={() => {
                      if (!lifecycleSubmitting) setPendingLifecycleAction(null)
                    }}
                  >
                    <Space.Compact size="small">
                      <Button
                        type="primary"
                        loading={updatingNow}
                        disabled={lifecycleSubmitting}
                        onClick={handleUpdateClick}
                      >
                        {updatingNow ? t.home.updating : t.home.updateBtn}
                      </Button>
                      <Dropdown
                        trigger={['click']}
                        placement="bottomRight"
                        disabled={updatingNow || lifecycleSubmitting}
                        menu={{ items: extendedMenuItems }}
                      >
                        <Button
                          type="primary"
                          disabled={updatingNow || lifecycleSubmitting}
                          aria-label={t.home.moreActionsAria}
                          icon={<DownOutlined />}
                        />
                      </Dropdown>
                    </Space.Compact>
                  </Popconfirm>
                </ConfigProvider>
              )}
            </Space>
          )}
        </Flex>
        </div>
      </Card>

      <ContainerMetaModal
        open={metaOpen}
        context={metaCtx}
        allTags={allTags}
        onClose={() => setMetaOpen(false)}
        onSaved={onChanged}
      />

      {/* 单容器谨慎更新确认弹窗 */}
      <Modal
        open={Boolean(cautionTargetSvc)}
        title={t.home.cautionConfirmTitle}
        okText={t.home.cautionConfirmBtn}
        cancelText={t.ignoreModal.cancel}
        okButtonProps={{ danger: true, loading: Boolean(updatingSvcKey) }}
        onOk={() => cautionTargetSvc && requestSingleServiceUpdate(cautionTargetSvc)}
        onCancel={() => setCautionTargetSvc(null)}
      >
        <Flex vertical gap="middle">
          <Alert
            type="warning"
            showIcon
            title={cautionTargetSvc?.reasonText || app.reasonText || t.home.cautionConfirmTitle}
          />
          <Typography.Text>{t.home.cautionConfirmDesc}</Typography.Text>
        </Flex>
      </Modal>

      <StoppedUpdateModal
        open={Boolean(stoppedUpdateSvc)}
        title={t.lifecycle.stoppedSingleTitle}
        description={t.lifecycle.stoppedSingleDesc.replace(
          '{name}',
          stoppedUpdateSvc?.name && stoppedUpdateSvc.name !== 'main'
            ? stoppedUpdateSvc.name
            : displayTitle,
        )}
        loadingChoice={stoppedLoadingChoice}
        onConfirm={(startAfter) => {
          if (stoppedUpdateSvc) {
            void triggerSingleServiceUpdate(stoppedUpdateSvc, startAfter)
          }
        }}
        onCancel={() => setStoppedUpdateSvc(null)}
      />

      {/* 双栏版本选择与按新版配置更新一体化弹窗 */}
      <VersionPickerModal
        open={Boolean(pickerSvc)}
        appName={pickerSvc?.name || app.name}
        subtitle={subtitle}
        service={pickerSvc}
        app={app}
        initialTab={pickerInitialTab}
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
          setPickerInitialTab('version')
          setPickerSvc(svc)
        }}
        onOpenComposeYaml={() => {
          setDetailOpen(false)
          handleUpdateByConfigClick()
        }}
        onOpenDelete={
          app.source !== 'vendor_managed'
            ? () => {
                setDetailOpen(false)
                setDeleteOpen(true)
              }
            : undefined
        }
      />

      {/* 删除容器 / 整项目确认弹窗 */}
      <DeleteAppModal
        open={deleteOpen}
        appKey={app.key}
        appName={isProjectApp ? projectName || app.appName || app.name : app.name}
        hostName={app.hostName}
        source={app.source}
        isProjectHint={isProjectApp}
        onClose={() => setDeleteOpen(false)}
        onDeleted={onChanged}
      />
    </>
  )
}
