import { useCallback, useMemo, useState } from 'react'
import {
  Alert,
  Avatar,
  Badge,
  Button,
  Card,
  ConfigProvider,
  Dropdown,
  Flex,
  Modal,
  Progress,
  Space,
  Tag,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import { ArrowRightOutlined, DownOutlined, SyncOutlined } from '@ant-design/icons'
import {
  api,
  resolveIconUrl,
  showApiError,
  type AppCardItem,
  type ServiceItem,
  type UpdateJobItem,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import DeleteAppModal from './DeleteAppModal'
import DetailDrawer from './DetailDrawer'
import IgnorePolicyModal from './IgnorePolicyModal'
import ProjectUpdateModal from './ProjectUpdateModal'
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

const avatarColors = ['#1677ff', '#13c2c2', '#722ed1', '#eb2f96', '#fa8c16', '#52c41a']

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
  const updateQueueJobs = useUIStore((s) => s.updateQueueJobs)
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)
  const setActiveLogJobId = useUIStore((s) => s.setActiveLogJobId)

  const iconSrc = resolveIconUrl(app.iconUrl)
  const [failedIconSrc, setFailedIconSrc] = useState<string | null>(null)
  const imgErr = Boolean(iconSrc && failedIconSrc === iconSrc)

  const [pickerSvc, setPickerSvc] = useState<ServiceItem | null>(null)
  const [pickerInitialTab, setPickerInitialTab] = useState<'version' | 'config'>('version')
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [projectInitialTab, setProjectInitialTab] = useState<'direct' | 'yaml'>('direct')
  const [detailOpen, setDetailOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [cautionTargetSvc, setCautionTargetSvc] = useState<ServiceItem | null>(null)
  const [updatingSvcKey, setUpdatingSvcKey] = useState('')
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
      showApiError(e)
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

  const deleteMenuLabel = isProjectApp ? t.home.deleteProjectMenu : t.home.deleteContainerMenu
  const canEditConfig = isMultiContainer || Boolean(primaryService)
  const extendedMenuItems = useMemo(
    () => [
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
    [canEditConfig, deleteMenuLabel, handleUpdateByConfigClick, t.home.updateByNewConfigMenu],
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
  const hasValidIcon = Boolean(iconSrc && !imgErr)

  return (
    <>
      <Card
        size="small"
        styles={{
          body: {
            padding: '16px 18px',
            height: '100%',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'space-between',
          },
        }}
        style={{
          height: '100%',
          borderColor: isCaution && !isChecking ? token.colorWarningBorder : undefined,
        }}
      >
        {/* 上半区：图标名称副标题 + 单行版本状态 */}
        <div>
          <Flex align="center" gap={12}>
            <Avatar
              shape="square"
              size={42}
              src={
                hasValidIcon ? (
                  <img
                    src={iconSrc!}
                    alt={app.name}
                    onError={() => setFailedIconSrc(iconSrc ?? null)}
                    style={{ objectFit: 'contain' }}
                  />
                ) : undefined
              }
              onError={() => {
                setFailedIconSrc(iconSrc ?? null)
                return false
              }}
              style={{
                borderRadius: token.borderRadius,
                backgroundColor: hasValidIcon ? token.colorBgContainer : pickColor(app.name),
                border: hasValidIcon ? `1px solid ${token.colorBorderSecondary}` : 'none',
                padding: hasValidIcon ? 5 : 0,
                flexShrink: 0,
                fontWeight: 600,
                opacity: isChecking ? 0.85 : 1,
              }}
            >
              {getInitials(app.name)}
            </Avatar>

            <div style={{ minWidth: 0, flex: 1 }}>
              <Flex align="center" gap={6} style={{ minWidth: 0 }}>
                <Typography.Text strong ellipsis={{ tooltip: app.name }} style={{ fontSize: 15 }}>
                  {app.name}
                </Typography.Text>
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

          {/* 中部单行版本区 */}
          <div style={{ marginTop: 14 }}>
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

          {failedJobsForApp.map((job) => (
            <Alert
              key={job.id}
              type="error"
              showIcon
              action={
                <Typography.Link
                  onClick={() => setActiveLogJobId(job.id)}
                  style={{ fontSize: token.fontSizeSM, whiteSpace: 'nowrap' }}
                >
                  {t.home.viewLogs}
                </Typography.Link>
              }
              title={
                <Typography.Text style={{ fontSize: token.fontSizeSM, wordBreak: 'break-word' }}>
                  {t.home.updateFailed}
                  {job.errorMsg || job.errorCode}
                </Typography.Text>
              }
              style={{ marginTop: 10, padding: '6px 10px' }}
            />
          ))}
        </div>

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
              {app.source !== 'vendor_managed' &&
                (primaryService && primaryService.kind !== 'uncomparable' ? (
                  <Space.Compact size="small">
                    <Button
                      disabled={updatingNow}
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
                      disabled={updatingNow}
                      menu={{ items: extendedMenuItems }}
                    >
                      <Button
                        disabled={updatingNow}
                        aria-label={t.home.moreActionsAria}
                        icon={<DownOutlined />}
                      />
                    </Dropdown>
                  </Space.Compact>
                ) : (
                  <Dropdown
                    trigger={['click']}
                    placement="bottomRight"
                    disabled={updatingNow}
                    menu={{ items: extendedMenuItems }}
                  >
                    <Button
                      size="small"
                      disabled={updatingNow}
                      aria-label={t.home.moreActionsAria}
                      icon={<DownOutlined />}
                    />
                  </Dropdown>
                ))}
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
                  <Space.Compact size="small">
                    <Button
                      type="primary"
                      loading={updatingNow}
                      onClick={handleUpdateClick}
                    >
                      {updatingNow ? t.home.updating : t.home.updateBtn}
                    </Button>
                    <Dropdown
                      trigger={['click']}
                      placement="bottomRight"
                      disabled={updatingNow}
                      menu={{ items: extendedMenuItems }}
                    >
                      <Button
                        type="primary"
                        disabled={updatingNow}
                        aria-label={t.home.moreActionsAria}
                        icon={<DownOutlined />}
                      />
                    </Dropdown>
                  </Space.Compact>
                </ConfigProvider>
              )}
            </Space>
          )}
        </Flex>
      </Card>

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
        <Flex vertical gap="middle">
          <Alert
            type="warning"
            showIcon
            title={cautionTargetSvc?.reasonText || app.reasonText || t.home.cautionConfirmTitle}
          />
          <Typography.Text>{t.home.cautionConfirmDesc}</Typography.Text>
        </Flex>
      </Modal>

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
