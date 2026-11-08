import { useEffect, useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Col,
  Collapse,
  Empty,
  Flex,
  Progress,
  Row,
  Segmented,
  Space,
  Typography,
} from 'antd'
import { SyncOutlined, WarningOutlined } from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type AppCardItem, type CheckProgress, type UpdateJobItem } from '../api/client'
import AppCard from '../components/AppCard'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

export default function HomePage() {
  const { locale, t } = useI18n()
  const qc = useQueryClient()
  const [hostFilter, setHostFilter] = useState<string>('')
  const [liveProgress, setLiveProgress] = useState<CheckProgress | null>(null)

  const overview = useQuery({
    queryKey: ['overview', hostFilter, locale],
    queryFn: () => api.overview(hostFilter || undefined),
  })

  // 订阅 SSE 实时检查与更新任务进度
  useEffect(() => {
    const es = new EventSource('/api/v1/events')
    es.addEventListener('progress', (ev) => {
      try {
        const p = JSON.parse((ev as MessageEvent).data) as CheckProgress
        setLiveProgress(p)
        if (!p.running && p.phase === 'done') {
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['checkRuns'] })
        }
      } catch {
        // ignore
      }
    })
    es.addEventListener('update_job', (ev) => {
      try {
        const job = JSON.parse((ev as MessageEvent).data) as UpdateJobItem
        useUIStore.getState().upsertUpdateJob(job)
        if (job.status === 'succeeded' || job.status === 'failed') {
          qc.invalidateQueries({ queryKey: ['overview'] })
          qc.invalidateQueries({ queryKey: ['updates'] })
        }
      } catch {
        // ignore
      }
    })
    return () => es.close()
  }, [qc])

  const progress = liveProgress ?? overview.data?.progress
  const isRunning = progress?.running ?? false

  // 浏览器标题角标展示待更新数量
  const normalList = overview.data?.normal ?? []
  const cautionList = overview.data?.caution ?? []
  const ignoredList = overview.data?.ignored ?? []
  const updateCount = normalList.length + cautionList.length

  useEffect(() => {
    document.title = updateCount > 0 ? `(${updateCount}) DKStore` : 'DKStore'
  }, [updateCount])

  const handleCheck = async () => {
    const p = await api.triggerCheck(hostFilter || undefined, true)
    setLiveProgress(p)
  }

  const formatTime = (ts?: number) => {
    if (!ts) return t.home.neverChecked
    return `${t.home.lastChecked}${new Date(ts * 1000).toLocaleString(locale)}`
  }

  const renderGrid = (items: AppCardItem[], emptyText: string) => {
    if (items.length === 0) {
      return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} />
    }
    return (
      <Row gutter={[16, 16]}>
        {items.map((item) => (
          <Col key={`${item.group}-${item.key}`} xs={24} md={12} lg={8}>
            <AppCard
              app={item}
              onChanged={() => qc.invalidateQueries({ queryKey: ['overview'] })}
            />
          </Col>
        ))}
      </Row>
    )
  }

  const hosts = overview.data?.hosts ?? []
  const failed = overview.data?.failed ?? []

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center" wrap gap={12}>
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            {t.home.title}
          </Typography.Title>
          <Space size={12} wrap>
            <Typography.Text type="secondary">
              {formatTime(overview.data?.lastCheckedAt)}
            </Typography.Text>
            {(overview.data?.totalAppCount ?? 0) > 0 && (
              <Typography.Text type="secondary">
                ·{' '}
                {t.home.summaryCounts
                  .replace('{total}', String(overview.data?.totalAppCount ?? 0))
                  .replace('{upToDate}', String(overview.data?.upToDateCount ?? 0))}
              </Typography.Text>
            )}
          </Space>
        </div>

        <Button
          type="primary"
          size="large"
          icon={<SyncOutlined spin={isRunning} />}
          loading={isRunning}
          onClick={handleCheck}
        >
          {isRunning ? t.home.checking : t.home.checkNow}
        </Button>
      </Flex>

      {isRunning && progress && (
        <Alert
          type="info"
          showIcon
          title={
            progress.phase === 'discovering'
              ? `${t.home.discoveringHost}${progress.hostName || ''}`
              : `${t.home.checkingProgress} (${progress.completed} / ${progress.total})`
          }
          description={
            progress.total > 0 ? (
              <Progress
                percent={Math.round((progress.completed / progress.total) * 100)}
                size="small"
                status="active"
              />
            ) : undefined
          }
        />
      )}

      {hosts.length > 1 && (
        <Segmented
          value={hostFilter}
          onChange={(v) => setHostFilter(String(v))}
          options={[
            { label: t.home.allHosts, value: '' },
            ...hosts.map((h) => ({ label: h.name, value: h.id })),
          ]}
        />
      )}

      {failed.length > 0 && (
        <Alert
          type="warning"
          showIcon
          icon={<WarningOutlined />}
          title={t.home.failedBannerTitle.replace('{count}', String(failed.length))}
          description={
            <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
              {failed.map((f) => (
                <li key={f.serviceKey}>
                  <Typography.Text strong>
                    {f.appName} ({f.hostName})
                  </Typography.Text>{' '}
                  <Typography.Text code>{f.imageRef}</Typography.Text> — {f.errorMsg}
                </li>
              ))}
            </ul>
          }
        />
      )}

      <Collapse
        ghost
        defaultActiveKey={['normal']}
        items={[
          {
            key: 'normal',
            label: (
              <Space>
                <Typography.Text strong style={{ fontSize: 16 }}>
                  {t.home.groupNormal}
                </Typography.Text>
                <Badge count={normalList.length} color="#2f6bff" showZero />
              </Space>
            ),
            children: renderGrid(normalList, t.home.emptyNormal),
          },
          {
            key: 'caution',
            label: (
              <Space>
                <Typography.Text strong style={{ fontSize: 16 }}>
                  {t.home.groupCaution}
                </Typography.Text>
                <Badge count={cautionList.length} color="#fa8c16" showZero />
              </Space>
            ),
            children: renderGrid(cautionList, t.home.emptyCaution),
          },
          {
            key: 'ignored',
            label: (
              <Space>
                <Typography.Text strong style={{ fontSize: 16 }}>
                  {t.home.groupIgnored}
                </Typography.Text>
                <Badge count={ignoredList.length} color="#8c8c8c" showZero />
              </Space>
            ),
            children: renderGrid(ignoredList, t.home.emptyIgnored),
          },
        ]}
      />
    </Space>
  )
}
