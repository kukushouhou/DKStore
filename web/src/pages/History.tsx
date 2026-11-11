import { useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Collapse,
  Descriptions,
  Empty,
  Flex,
  Modal,
  Progress,
  Segmented,
  Space,
  Tag,
  Timeline,
  Typography,
  message,
  theme,
} from 'antd'
import { ArrowRightOutlined, RollbackOutlined } from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type UpdateJobItem } from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

export default function HistoryPage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const [tab, setTab] = useState<'updates' | 'scans'>('updates')
  const [verifyingId, setVerifyingId] = useState<string | null>(null)
  const [rollbackModalJob, setRollbackModalJob] = useState<UpdateJobItem | null>(null)
  const [rollbackRef, setRollbackRef] = useState('')
  const [dataWarning, setDataWarning] = useState('')
  const [rollingBack, setRollingBack] = useState(false)

  const updatesQuery = useQuery({
    queryKey: ['updates', locale],
    queryFn: api.listUpdates,
    refetchInterval: 4000,
  })
  const runsQuery = useQuery({ queryKey: ['check-runs', locale], queryFn: api.checkRuns })

  const updateItems = updatesQuery.data?.items ?? []
  const scanItems = runsQuery.data?.items ?? []

  const triggerLabel = (tr: string) => {
    if (tr === 'manual') return t.history.triggerManual
    if (tr === 'schedule') return t.history.triggerSchedule
    return t.history.triggerStartup
  }

  const changeTag = (chg: string) => {
    if (chg === 'appeared') return <Tag color="processing">{t.history.changeAppeared}</Tag>
    if (chg === 'changed') return <Tag color="warning">{t.history.changeChanged}</Tag>
    return <Tag color="success">{t.history.changeResolved}</Tag>
  }

  const statusTag = (st: string) => {
    if (st === 'succeeded') return <Tag color="success">{t.history.statusSucceeded}</Tag>
    if (st === 'failed') return <Tag color="error">{t.history.statusFailed}</Tag>
    return <Tag color="processing">{t.history.statusRunning}</Tag>
  }

  const onVerifyRollback = async (job: UpdateJobItem) => {
    setVerifyingId(job.id)
    try {
      const res = await api.verifyRollback(job.id)
      qc.invalidateQueries({ queryKey: ['updates'] })
      if (!res.eligible) {
        Modal.warning({
          title: t.history.rollbackUnavailable,
          content: res.reasonText || job.rollbackReasonText || t.history.rollbackUnavailable,
        })
        return
      }
      setRollbackRef(res.rollbackRef || job.rollbackRef || job.oldImageRef)
      setDataWarning(res.dataWarning || '')
      setRollbackModalJob(job)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setVerifyingId(null)
    }
  }

  const onConfirmRollback = async () => {
    if (!rollbackModalJob) return
    setRollingBack(true)
    try {
      const rbJob = await api.executeRollback(rollbackModalJob.id)
      if (rbJob?.id) {
        useUIStore.getState().upsertUpdateJob(rbJob)
      }
      message.success(t.history.rollbackStarted)
      setRollbackModalJob(null)
      qc.invalidateQueries({ queryKey: ['updates'] })
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setRollingBack(false)
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center" wrap gap={12}>
        <Typography.Title level={3} style={{ margin: 0 }}>
          {t.history.title}
        </Typography.Title>
        <Segmented
          value={tab}
          onChange={(v) => setTab(v as 'updates' | 'scans')}
          options={[
            { label: t.history.tabUpdates, value: 'updates' },
            { label: t.history.tabChecks, value: 'scans' },
          ]}
        />
      </Flex>

      {tab === 'updates' ? (
        updateItems.length === 0 ? (
          <Card style={{ textAlign: 'center', padding: '32px 16px' }}>
            <Empty description={t.history.emptyUpdates} />
          </Card>
        ) : (
          <Space direction="vertical" size="middle" style={{ width: '100%' }}>
            {updateItems.map((job) => (
              <Card key={job.id} size="small">
                <Space direction="vertical" size={8} style={{ width: '100%' }}>
                  <Flex justify="space-between" align="center" wrap gap={8}>
                    <Space wrap>
                      <Tag color={job.kind === 'rollback' ? 'purple' : 'blue'}>
                        {job.kind === 'rollback' ? t.history.kindRollback : t.history.kindUpgrade}
                      </Tag>
                      <Typography.Text strong>
                        {job.appName}
                        {job.serviceName && job.serviceName !== 'main' && job.serviceName !== job.appName
                          ? ` / ${job.serviceName}`
                          : ''}
                      </Typography.Text>
                      <Typography.Text type="secondary">({job.hostName})</Typography.Text>
                      {statusTag(job.status)}
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        {new Date(job.startedAt * 1000).toLocaleString(locale)}
                      </Typography.Text>
                    </Space>

                    <Space wrap>
                      <Button
                        size="small"
                        onClick={() => useUIStore.getState().setActiveLogJobId(job.id)}
                      >
                        {t.home.viewLogs}
                      </Button>
                      {job.kind === 'upgrade' && job.status === 'succeeded' && (
                        <>
                          {!job.rollbackEligible && job.rollbackReasonText && (
                            <Tag color="default">
                              {t.history.rollbackUnavailable}: {job.rollbackReasonText}
                            </Tag>
                          )}
                          <Button
                            size="small"
                            icon={<RollbackOutlined />}
                            loading={verifyingId === job.id}
                            onClick={() => onVerifyRollback(job)}
                          >
                            {t.history.verifyRollbackBtn}
                          </Button>
                        </>
                      )}
                    </Space>
                  </Flex>

                  <Space size={6} wrap>
                    <Tag style={{ fontFamily: token.fontFamilyCode, margin: 0 }}>
                      {job.oldVersion || job.oldImageRef || t.home.unknownCurrent}
                    </Tag>
                    <ArrowRightOutlined style={{ fontSize: token.fontSizeSM }} />
                    <Tag
                      color="processing"
                      style={{ fontFamily: token.fontFamilyCode, fontWeight: 600, margin: 0 }}
                    >
                      {job.targetVersion || job.targetImageRef || t.home.newImageDetected}
                    </Tag>
                  </Space>

                  {job.status !== 'succeeded' && job.status !== 'failed' && (
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        {job.progress?.statusText || job.phase}
                      </Typography.Text>
                      <Progress percent={job.progress?.percent ?? 0} size="small" status="active" />
                    </div>
                  )}

                  {job.errorMsg && <Alert type="error" showIcon title={job.errorMsg} />}

                  <Collapse
                    ghost
                    size="small"
                    styles={{
                      header: { paddingInline: 0 },
                      body: { paddingInline: 0, paddingBottom: 0 },
                    }}
                    items={[
                      {
                        key: 'details',
                        label: t.history.logsLabel,
                        children: (
                          <Space direction="vertical" size="small" style={{ width: '100%' }}>
                            <Descriptions column={1} size="small">
                              <Descriptions.Item label={t.detail.imageRef}>
                                <Typography.Text code>{job.oldImageRef}</Typography.Text>{' '}
                                <ArrowRightOutlined style={{ fontSize: token.fontSizeSM }} />{' '}
                                <Typography.Text code>{job.targetImageRef}</Typography.Text>
                              </Descriptions.Item>
                              {job.oldRepoDigest && (
                                <Descriptions.Item label={t.detail.localDigest}>
                                  <Typography.Text
                                    code
                                    style={{ fontSize: token.fontSizeSM, wordBreak: 'break-all' }}
                                  >
                                    {job.oldRepoDigest}
                                  </Typography.Text>
                                </Descriptions.Item>
                              )}
                              {job.targetRepoDigest && (
                                <Descriptions.Item label={t.detail.targetDigest}>
                                  <Typography.Text
                                    code
                                    style={{ fontSize: token.fontSizeSM, wordBreak: 'break-all' }}
                                  >
                                    {job.targetRepoDigest}
                                  </Typography.Text>
                                </Descriptions.Item>
                              )}
                              {(job.oldMatchedTags?.length ?? 0) > 0 && (
                                <Descriptions.Item label={t.detail.matchedTags}>
                                  {job.oldMatchedTags?.join(', ')}
                                </Descriptions.Item>
                              )}
                            </Descriptions>
                            {job.logs && (
                              <pre
                                style={{
                                  margin: 0,
                                  padding: 10,
                                  fontFamily: token.fontFamilyCode,
                                  fontSize: token.fontSizeSM,
                                  background: token.colorFillAlter,
                                  border: `1px solid ${token.colorBorderSecondary}`,
                                  borderRadius: token.borderRadiusSM,
                                  whiteSpace: 'pre-wrap',
                                  wordBreak: 'break-all',
                                }}
                              >
                                {job.logs}
                              </pre>
                            )}
                          </Space>
                        ),
                      },
                    ]}
                  />
                </Space>
              </Card>
            ))}
          </Space>
        )
      ) : scanItems.length === 0 ? (
        <Card style={{ textAlign: 'center', padding: '32px 16px' }}>
          <Empty description={t.history.empty} />
        </Card>
      ) : (
        <Timeline
          items={scanItems.map((r) => ({
            key: r.id,
            children: (
              <Card size="small" style={{ marginBottom: 8 }}>
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Space wrap>
                    <Typography.Text strong>
                      {new Date(r.startedAt * 1000).toLocaleString(locale)}
                    </Typography.Text>
                    <Tag>{triggerLabel(r.trigger)}</Tag>
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {t.history.summary
                        .replace('{total}', String(r.summary?.total ?? 0))
                        .replace('{normal}', String(r.summary?.normal ?? 0))
                        .replace('{caution}', String(r.summary?.caution ?? 0))
                        .replace('{failed}', String(r.summary?.failed ?? 0))}
                    </Typography.Text>
                  </Space>
                  {r.changes && r.changes.length > 0 ? (
                    <Space direction="vertical" size={4}>
                      {r.changes.map((c, idx) => (
                        <Space key={idx} size={6} wrap>
                          {changeTag(c.change)}
                          <Typography.Text strong>
                            {c.appName}
                            {c.serviceName && c.serviceName !== 'main' && c.serviceName !== c.appName
                              ? `/${c.serviceName}`
                              : ''}
                          </Typography.Text>
                          <Typography.Text type="secondary">({c.hostName})</Typography.Text>
                          {c.targetVersion && (
                            <Space size={4}>
                              <Typography.Text code>
                                {c.currentVersion || t.home.unknownCurrent}
                              </Typography.Text>
                              <ArrowRightOutlined style={{ fontSize: token.fontSizeSM }} />
                              <Typography.Text code>{c.targetVersion}</Typography.Text>
                            </Space>
                          )}
                        </Space>
                      ))}
                    </Space>
                  ) : (
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {t.history.noChanges}
                    </Typography.Text>
                  )}
                </Space>
              </Card>
            ),
          }))}
        />
      )}

      <Modal
        open={Boolean(rollbackModalJob)}
        title={t.history.rollbackModalTitle}
        okText={t.history.rollbackConfirmBtn}
        cancelText={t.ignoreModal.cancel}
        confirmLoading={rollingBack}
        onOk={onConfirmRollback}
        onCancel={() => setRollbackModalJob(null)}
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label={t.history.rollbackTargetLabel}>
              <Typography.Text code>{rollbackRef}</Typography.Text>
            </Descriptions.Item>
          </Descriptions>
          {dataWarning && <Alert type="warning" showIcon title={dataWarning} />}
        </Space>
      </Modal>
    </Space>
  )
}
