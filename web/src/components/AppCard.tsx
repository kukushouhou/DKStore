import { useState } from 'react'
import {
  Alert,
  Avatar,
  Button,
  Card,
  Collapse,
  Descriptions,
  Drawer,
  Flex,
  Grid,
  Modal,
  Progress,
  Radio,
  Space,
  Tag,
  Typography,
  message,
} from 'antd'
import {
  ArrowRightOutlined,
  CloudDownloadOutlined,
  CopyOutlined,
  DatabaseOutlined,
  EyeInvisibleOutlined,
  FileTextOutlined,
  GithubOutlined,
  InfoCircleOutlined,
  RollbackOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { api, type AppCardItem, type ServiceItem, type UpdateJobItem } from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

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

const avatarColors = ['#2f6bff', '#13c2c2', '#722ed1', '#eb2f96', '#fa8c16', '#52c41a']

function pickColor(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) {
    h = (h * 31 + name.charCodeAt(i)) | 0
  }
  return avatarColors[Math.abs(h) % avatarColors.length]
}

interface Props {
  app: AppCardItem
  onChanged: () => void
}

export default function AppCard({ app, onChanged }: Props) {
  const { t } = useI18n()
  const screens = Grid.useBreakpoint()
  const activeUpdateJobs = useUIStore((s) => s.activeUpdateJobs)
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  const [ignoreMode, setIgnoreMode] = useState<'this_version' | 'forever'>('this_version')
  const [ignoreTargetKeys, setIgnoreTargetKeys] = useState<string[]>([])
  const [ignoreTargetName, setIgnoreTargetName] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [detailOpen, setDetailOpen] = useState(false)
  const [cautionTargetSvc, setCautionTargetSvc] = useState<ServiceItem | null>(null)
  const [updatingSvcKey, setUpdatingSvcKey] = useState('')

  const activeJobsForApp: UpdateJobItem[] = app.services
    .map((s) => activeUpdateJobs[s.key])
    .filter((j): j is UpdateJobItem => Boolean(j))

  const triggerServiceUpdate = async (svc: ServiceItem) => {
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

  const onClickUpdateService = (svc: ServiceItem) => {
    if (svc.category === 'caution' || app.group === 'caution' || app.isDatabase) {
      setCautionTargetSvc(svc)
      return
    }
    triggerServiceUpdate(svc)
  }

  const onClickUpdateCard = () => {
    const updatable = app.services.filter(
      (s) => (s.kind === 'digest_update' || s.kind === 'version_update') && !s.ignored,
    )
    if (updatable.length === 1) {
      onClickUpdateService(updatable[0])
    } else {
      setDetailOpen(true)
    }
  }

  const openIgnoreForApp = () => {
    const keys = app.updateServiceKeys?.length ? app.updateServiceKeys : app.services.map((s) => s.key)
    setIgnoreTargetKeys(keys)
    setIgnoreTargetName(app.name)
    setIgnoreMode('this_version')
    setIgnoreOpen(true)
  }

  const openIgnoreForService = (svc: ServiceItem) => {
    setIgnoreTargetKeys([svc.key])
    setIgnoreTargetName(`${app.name} / ${svc.name}`)
    setIgnoreMode('this_version')
    setIgnoreOpen(true)
  }

  const submitIgnore = async () => {
    setSubmitting(true)
    try {
      await api.setIgnore(ignoreTargetKeys, ignoreMode)
      message.success(t.ignoreModal.success)
      setIgnoreOpen(false)
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const restoreApp = async () => {
    try {
      await api.removeIgnoreByApp(app.key)
      message.success(t.ignoreModal.restored)
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const restoreService = async (svcKey: string) => {
    try {
      await api.removeIgnoreByService(svcKey)
      message.success(t.ignoreModal.restored)
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const sourceLabel = () => {
    switch (app.source) {
      case 'compose':
        return t.home.tagCompose
      case 'dsm_project':
        return t.home.tagDSM
      case 'vendor_managed':
        return t.home.tagVendor
      default:
        return t.home.tagStandalone
    }
  }

  const curVer = app.summaryCurrent || t.home.unknownCurrent
  const tgtVer = app.summaryTarget || t.home.newImageDetected
  const releaseUrl = app.links.releases || app.links.repo

  return (
    <>
      <Card
        hoverable
        style={{ height: '100%' }}
        onClick={() => setDetailOpen(true)}
      >
        <Flex vertical gap={12}>
          <Flex justify="space-between" align="flex-start" gap={12}>
            <Flex align="center" gap={12} style={{ minWidth: 0 }}>
              <Avatar
                size={44}
                shape="square"
                style={{
                  backgroundColor: pickColor(app.name),
                  borderRadius: 10,
                  fontWeight: 600,
                  flexShrink: 0,
                }}
              >
                {app.name.slice(0, 2).toUpperCase()}
              </Avatar>
              <div style={{ minWidth: 0 }}>
                <Space size={6} wrap>
                  <Typography.Text strong style={{ fontSize: 16 }}>
                    {app.name}
                  </Typography.Text>
                  {app.isDatabase && (
                    <Tag color="warning" icon={<DatabaseOutlined />}>
                      {app.databaseName || t.home.tagDatabase}
                    </Tag>
                  )}
                  {app.group === 'ignored' && (
                    <Tag>
                      {app.ignoreMode === 'forever' ? t.home.ignoredForever : t.home.ignoredThisVersion}
                    </Tag>
                  )}
                </Space>
                <div>
                  <Space size={6} wrap>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {app.hostName}
                    </Typography.Text>
                    <Tag variant="filled" style={{ fontSize: 11, margin: 0 }}>
                      {sourceLabel()}
                    </Tag>
                    {app.services.length > 1 && (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        · {t.home.servicesCount.replace('{count}', String(app.services.length))}
                      </Typography.Text>
                    )}
                    {app.hostStatus === 'offline' && (
                      <Tag color="error" style={{ fontSize: 11, margin: 0 }}>
                        {t.home.tagOffline}
                      </Tag>
                    )}
                  </Space>
                </div>
              </div>
            </Flex>
          </Flex>

          <Flex align="center" gap={8} wrap>
            <Tag style={{ margin: 0, fontFamily: 'monospace' }}>{curVer}</Tag>
            <ArrowRightOutlined style={{ fontSize: 12, opacity: 0.6 }} />
            <Tag
              color={app.group === 'caution' ? 'warning' : 'processing'}
              style={{ margin: 0, fontFamily: 'monospace', fontWeight: 600 }}
            >
              {tgtVer}
            </Tag>
          </Flex>

          {app.reasonText && (
            <Alert
              type={app.group === 'caution' ? 'warning' : 'info'}
              showIcon
              icon={app.group === 'caution' ? <WarningOutlined /> : <InfoCircleOutlined />}
              title={<span style={{ fontSize: 12 }}>{app.reasonText}</span>}
              style={{ padding: '6px 10px' }}
            />
          )}

          {activeJobsForApp.map((job) => (
            <div key={job.id} style={{ width: '100%' }}>
              <Flex justify="space-between" align="center">
                <Typography.Text style={{ fontSize: 12 }} type="secondary">
                  {job.progress?.statusText || job.phase}
                </Typography.Text>
                {(job.progress?.totalLayers ?? 0) > 0 && (
                  <Typography.Text style={{ fontSize: 11 }} type="secondary">
                    {t.home.layerStat
                      .replace('{done}', String(job.progress?.completedLayers ?? 0))
                      .replace('{total}', String(job.progress?.totalLayers ?? 0))}
                    {(job.progress?.totalBytes ?? 0) > 0
                      ? ` (${formatBytes(job.progress?.downloadedBytes ?? 0)} / ${formatBytes(job.progress?.totalBytes ?? 0)})`
                      : ''}
                  </Typography.Text>
                )}
              </Flex>
              <Progress percent={job.progress?.percent ?? 0} size="small" status="active" />
            </div>
          ))}

          <Flex
            justify="space-between"
            align="center"
            wrap
            gap={8}
            onClick={(e) => e.stopPropagation()}
          >
            <Space size={4} wrap>
              {releaseUrl && (
                <Button
                  size="small"
                  type="link"
                  icon={<FileTextOutlined />}
                  href={releaseUrl}
                  target="_blank"
                  rel="noreferrer"
                  style={{ paddingInline: 4 }}
                >
                  {app.links.releases ? t.home.viewReleaseNotes : t.home.viewRepo}
                </Button>
              )}
              <Button
                size="small"
                type="link"
                onClick={() => setDetailOpen(true)}
                style={{ paddingInline: 4 }}
              >
                {t.home.howToUpdate}
              </Button>
            </Space>

            <Space size={6} wrap>
              {app.group === 'ignored' ? (
                <Button size="small" icon={<RollbackOutlined />} onClick={restoreApp}>
                  {t.home.restoreReminder}
                </Button>
              ) : (
                <>
                  <Button size="small" icon={<EyeInvisibleOutlined />} onClick={openIgnoreForApp}>
                    {t.home.ignore}
                  </Button>
                  {app.source === 'vendor_managed' ? (
                    <Tag>{t.home.vendorReadonlyTip}</Tag>
                  ) : (
                    <Button
                      size="small"
                      type="primary"
                      danger={app.group === 'caution'}
                      icon={<CloudDownloadOutlined />}
                      loading={activeJobsForApp.length > 0 || Boolean(updatingSvcKey)}
                      onClick={onClickUpdateCard}
                    >
                      {activeJobsForApp.length > 0 ? t.home.updating : t.home.updateNow}
                    </Button>
                  )}
                </>
              )}
            </Space>
          </Flex>
        </Flex>
      </Card>

      <Modal
        open={Boolean(cautionTargetSvc)}
        title={t.home.cautionConfirmTitle}
        okText={t.home.cautionConfirmBtn}
        cancelText={t.ignoreModal.cancel}
        okButtonProps={{ danger: true, loading: Boolean(updatingSvcKey) }}
        onOk={() => cautionTargetSvc && triggerServiceUpdate(cautionTargetSvc)}
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

      <Modal
        open={ignoreOpen}
        title={t.ignoreModal.title}
        okText={t.ignoreModal.confirm}
        cancelText={t.ignoreModal.cancel}
        confirmLoading={submitting}
        onOk={submitIgnore}
        onCancel={() => setIgnoreOpen(false)}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Typography.Text>
            {t.ignoreModal.desc.replace('{name}', ignoreTargetName)}
          </Typography.Text>
          <Radio.Group
            value={ignoreMode}
            onChange={(e) => setIgnoreMode(e.target.value)}
            style={{ display: 'flex', flexDirection: 'column', gap: 12 }}
          >
            <Radio value="this_version">{t.ignoreModal.thisVersion}</Radio>
            <Radio value="forever">{t.ignoreModal.forever}</Radio>
          </Radio.Group>
        </Space>
      </Modal>

      <Drawer
        open={detailOpen}
        title={`${app.name} · ${app.hostName}`}
        placement={screens.md ? 'right' : 'bottom'}
        width={screens.md ? 600 : undefined}
        height={screens.md ? undefined : '82vh'}
        onClose={() => setDetailOpen(false)}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {app.services.map((svc) => {
            const hasUpdate = svc.kind === 'digest_update' || svc.kind === 'version_update'
            const cmd =
              app.source === 'compose' || app.source === 'dsm_project'
                ? `docker compose pull ${svc.name} && docker compose up -d --no-deps ${svc.name}`
                : `docker pull ${svc.targetRef || svc.imageRef}`

            return (
              <Card key={svc.key} size="small" title={svc.name} extra={<Tag>{svc.state}</Tag>}>
                <Space direction="vertical" size="small" style={{ width: '100%' }}>
                  <Flex justify="space-between" align="center" wrap gap={8}>
                    {hasUpdate ? (
                      <Space size={6} wrap>
                        <Tag style={{ fontFamily: 'monospace' }}>
                          {svc.currentVersion || t.home.unknownCurrent}
                        </Tag>
                        <ArrowRightOutlined style={{ fontSize: 12 }} />
                        <Tag
                          color={svc.category === 'caution' ? 'warning' : 'processing'}
                          style={{ fontFamily: 'monospace', fontWeight: 600 }}
                        >
                          {svc.targetVersion || t.home.newImageDetected}
                        </Tag>
                      </Space>
                    ) : svc.kind === 'uncomparable' ? (
                      <Typography.Text type="secondary">{t.detail.uncomparable}</Typography.Text>
                    ) : (
                      <Tag color="success">{t.detail.upToDate}</Tag>
                    )}

                    <Space size={6}>
                      {svc.links.repo && (
                        <Button
                          size="small"
                          icon={<GithubOutlined />}
                          href={svc.links.releases || svc.links.repo}
                          target="_blank"
                          rel="noreferrer"
                        >
                          {svc.links.releases ? t.home.viewReleaseNotes : t.home.viewRepo}
                        </Button>
                      )}
                      {svc.ignored ? (
                        <Button
                          size="small"
                          icon={<RollbackOutlined />}
                          onClick={() => restoreService(svc.key)}
                        >
                          {t.home.restoreReminder}
                        </Button>
                      ) : (
                        hasUpdate && (
                          <>
                            <Button
                              size="small"
                              icon={<EyeInvisibleOutlined />}
                              onClick={() => openIgnoreForService(svc)}
                            >
                              {t.home.ignore}
                            </Button>
                            {app.source === 'vendor_managed' ? (
                              <Tag>{t.home.vendorReadonlyTip}</Tag>
                            ) : (
                              <Button
                                size="small"
                                type="primary"
                                danger={svc.category === 'caution'}
                                icon={<CloudDownloadOutlined />}
                                loading={
                                  Boolean(activeUpdateJobs[svc.key]) || updatingSvcKey === svc.key
                                }
                                onClick={() => onClickUpdateService(svc)}
                              >
                                {activeUpdateJobs[svc.key] ? t.home.updating : t.home.updateNow}
                              </Button>
                            )}
                          </>
                        )
                      )}
                    </Space>
                  </Flex>

                  {activeUpdateJobs[svc.key] && (
                    <div>
                      <Typography.Text style={{ fontSize: 12 }} type="secondary">
                        {activeUpdateJobs[svc.key].progress?.statusText || activeUpdateJobs[svc.key].phase}
                      </Typography.Text>
                      <Progress
                        percent={activeUpdateJobs[svc.key].progress?.percent ?? 0}
                        size="small"
                        status="active"
                      />
                    </div>
                  )}

                  {svc.reasonText && (
                    <Alert
                      type={svc.category === 'caution' ? 'warning' : 'info'}
                      showIcon
                      title={svc.reasonText}
                    />
                  )}
                  {svc.hint && <Alert type="info" showIcon title={svc.hint} />}
                  {svc.errorMsg && <Alert type="error" showIcon title={svc.errorMsg} />}

                  {hasUpdate && (
                    <Card
                      size="small"
                      type="inner"
                      title={t.detail.howToUpdateTitle}
                      extra={
                        <Button
                          size="small"
                          type="text"
                          icon={<CopyOutlined />}
                          onClick={() => {
                            navigator.clipboard.writeText(cmd)
                            message.success(t.detail.copied)
                          }}
                        >
                          {t.detail.copyCmd}
                        </Button>
                      }
                    >
                      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
                        {t.detail.howToUpdateDesc}
                      </Typography.Paragraph>
                      <Typography.Text code style={{ fontSize: 12, wordBreak: 'break-all' }}>
                        {cmd}
                      </Typography.Text>
                    </Card>
                  )}

                  <Collapse
                    ghost
                    size="small"
                    items={[
                      {
                        key: 'tech',
                        label: t.detail.techInfo,
                        children: (
                          <Descriptions column={1} size="small">
                            <Descriptions.Item label={t.detail.imageRef}>
                              <Typography.Text code>{svc.imageRef}</Typography.Text>
                            </Descriptions.Item>
                            {svc.repoDigest && (
                              <Descriptions.Item label={t.detail.localDigest}>
                                <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                                  {svc.repoDigest}
                                </Typography.Text>
                              </Descriptions.Item>
                            )}
                            {svc.targetDigest && (
                              <Descriptions.Item label={t.detail.targetDigest}>
                                <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                                  {svc.targetDigest}
                                </Typography.Text>
                              </Descriptions.Item>
                            )}
                            {(svc.targetTags?.length ?? 0) > 0 && (
                              <Descriptions.Item label={t.detail.matchedTags}>
                                {svc.targetTags?.join(', ')}
                              </Descriptions.Item>
                            )}
                          </Descriptions>
                        ),
                      },
                    ]}
                  />
                </Space>
              </Card>
            )
          })}
        </Space>
      </Drawer>
    </>
  )
}
