import { useRef, useState } from 'react'
import { Button, Flex, Grid, Modal, Space, Tag, Typography, message, theme } from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CopyOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api, type UpdateJobItem } from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import TerminalLogBox, { type TerminalLogBoxHandle } from './TerminalLogBox'

export default function UpdateLogModal() {
  const jobId = useUIStore((s) => s.activeLogJobId)
  const setActiveLogJobId = useUIStore((s) => s.setActiveLogJobId)
  const screens = Grid.useBreakpoint()

  return (
    <Modal
      open={Boolean(jobId)}
      onCancel={() => setActiveLogJobId(null)}
      footer={null}
      width={screens.md ? 820 : '96vw'}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {jobId && <UpdateLogModalBody key={jobId} jobId={jobId} onClose={() => setActiveLogJobId(null)} />}
    </Modal>
  )
}

function UpdateLogModalBody({ jobId, onClose }: { jobId: string; onClose: () => void }) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const qc = useQueryClient()
  const liveJob = useUIStore((s) => s.updateQueueJobs[jobId])
  const activeUpdateJobs = useUIStore((s) => s.activeUpdateJobs)
  const [fetchedJob, setFetchedJob] = useState<UpdateJobItem | null>(null)
  const [retrying, setRetrying] = useState(false)
  const termBoxRef = useRef<TerminalLogBoxHandle | null>(null)

  const job = liveJob ?? fetchedJob
  const status = job?.status ?? 'running'

  const handleCopyLogs = async () => {
    await termBoxRef.current?.copyLogs()
  }

  const displayName =
    job?.serviceName && job.serviceName !== 'main' && job.serviceName !== job.appName
      ? `${job.appName} (${job.serviceName})`
      : job?.appName || jobId
  const titleText = t.logModal.title.replace('{name}', displayName)
  const hasRunningSameTarget = Boolean(
    job &&
      ((job.serviceKey && activeUpdateJobs[job.serviceKey]) ||
        (job.appKey && activeUpdateJobs[job.appKey])),
  )
  const canRetry =
    status === 'failed' && job?.retryable !== false && !hasRunningSameTarget

  const handleRetry = async () => {
    setRetrying(true)
    try {
      const next = await api.retryUpdateJob(jobId)
      const st = useUIStore.getState()
      st.dismissQueueJob(jobId)
      st.upsertUpdateJob(next)
      st.setActiveLogJobId(next.id)
      void qc.invalidateQueries({ queryKey: ['updates'] })
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setRetrying(false)
    }
  }

  const renderStatusBadge = () => {
    if (status === 'succeeded') {
      return (
        <Tag color="success" icon={<CheckCircleOutlined />} style={{ margin: 0 }}>
          {t.logModal.badgeSucceeded}
        </Tag>
      )
    }
    if (status === 'failed') {
      return (
        <Tag color="error" icon={<CloseCircleOutlined />} style={{ margin: 0 }}>
          {t.logModal.badgeFailed}
        </Tag>
      )
    }
    return (
      <Tag color="processing" icon={<SyncOutlined spin />} style={{ margin: 0 }}>
        {t.logModal.badgeRunning}
      </Tag>
    )
  }

  return (
    <Flex vertical gap={12}>
      {/* 顶栏：左侧单行省略防挤压，右侧按钮组固定同行（失败时关闭退为次按钮、重试居最右主按钮位） */}
      <Flex
        align="flex-start"
        justify="space-between"
        gap={12}
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <div style={{ minWidth: 0, flex: 1 }}>
          <Flex align="center" gap={8} style={{ minWidth: 0 }}>
            <Typography.Title
              level={5}
              ellipsis={{ tooltip: titleText }}
              style={{ margin: 0, minWidth: 0 }}
            >
              {titleText}
            </Typography.Title>
            <span style={{ flexShrink: 0, display: 'inline-flex' }}>{renderStatusBadge()}</span>
          </Flex>
          {job && (
            <Typography.Text
              type="secondary"
              ellipsis={{ tooltip: `${job.hostName} · ${job.targetImageRef}` }}
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
            >
              {job.hostName} · <Typography.Text code>{job.targetImageRef}</Typography.Text>
            </Typography.Text>
          )}
        </div>

        <Space size={8} style={{ flexShrink: 0 }}>
          <Button size="small" icon={<CopyOutlined />} onClick={handleCopyLogs}>
            {t.logModal.copyBtn}
          </Button>
          <Button
            size="small"
            type={canRetry ? 'default' : 'primary'}
            onClick={onClose}
          >
            {t.logModal.closeBtn}
          </Button>
          {canRetry && (
            <Button size="small" type="primary" loading={retrying} onClick={handleRetry}>
              {t.queue.retryBtn}
            </Button>
          )}
        </Space>
      </Flex>

      {/* 共用只读 xterm.js 终端视口 */}
      <TerminalLogBox
        ref={termBoxRef}
        jobId={jobId}
        status={status}
        fallbackMeta={job ?? undefined}
        height={400}
        onJobLoaded={setFetchedJob}
      />
    </Flex>
  )
}
