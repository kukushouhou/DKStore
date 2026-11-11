import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Card,
  Col,
  Collapse,
  Empty,
  Flex,
  Form,
  Input,
  Modal,
  Row,
  Segmented,
  Space,
  Spin,
  Tag,
  Typography,
  message,
  theme,
} from 'antd'
import { PlusOutlined, SyncOutlined } from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, type AppCardItem, type FailedItem } from '../api/client'
import AppCard from '../components/AppCard'
import UpdateQueuePanel from '../components/UpdateQueuePanel'
import { useIsUltrawide, useOrderedQueueJobs } from '../hooks/useUpdateQueue'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

export default function HomePage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const navigate = useNavigate()

  const [hostFilter, setHostFilter] = useState<string>('')
  const liveProgress = useUIStore((s) => s.liveProgress)
  const setLiveProgress = useUIStore((s) => s.setLiveProgress)
  const queueCollapsed = useUIStore((s) => s.queueCollapsed)
  const isUltrawide = useIsUltrawide()
  const queueJobs = useOrderedQueueJobs()

  // 分组折叠状态：默认展开可更新、需谨慎更新（非空时自动展开）与无需更新
  const [normalExpanded, setNormalExpanded] = useState(true)
  const [cautionToggled, setCautionToggled] = useState<boolean | null>(null)
  const [noUpdateExpanded, setNoUpdateExpanded] = useState(true)
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000))

  // Docker Hub 快捷登录弹窗状态
  const [hubModalOpen, setHubModalOpen] = useState(false)
  const [hubForm] = Form.useForm<{ username: string; secret: string }>()
  const [hubSubmitting, setHubSubmitting] = useState(false)

  // 其他错误提示条展开状态
  const [failedExpanded, setFailedExpanded] = useState(false)

  const overview = useQuery({
    queryKey: ['overview', hostFilter, locale],
    queryFn: () => api.overview(hostFilter || undefined),
  })

  const progress = liveProgress ?? overview.data?.progress
  const isRunning = progress?.running ?? false

  const normalList = overview.data?.normal ?? []
  const cautionList = overview.data?.caution ?? []
  const noUpdateList = overview.data?.noUpdate ?? overview.data?.ignored ?? []
  const updateCount = normalList.length + cautionList.length

  useEffect(() => {
    document.title = updateCount > 0 ? `(${updateCount}) DKStore` : 'DKStore'
  }, [updateCount])

  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 30000)
    return () => clearInterval(id)
  }, [])

  const cautionExpanded = cautionToggled ?? cautionList.length > 0

  const completedKeySet = useMemo(
    () => new Set(progress?.completedServiceKeys ?? []),
    [progress?.completedServiceKeys],
  )

  const isCardChecking = (item: AppCardItem): boolean => {
    if (!isRunning) return false
    if (progress?.phase === 'discovering') return true
    if (progress?.phase === 'checking') {
      if (Array.isArray(progress.completedServiceKeys)) {
        const checkSvcs = item.services.length > 0 ? item.services : (item.allServices ?? [])
        if (checkSvcs.length === 0) return false
        return !checkSvcs.every((s) => completedKeySet.has(s.key))
      }
      if (progress.currentServiceKey) {
        return item.services.some((s) => s.key === progress.currentServiceKey)
      }
    }
    return false
  }

  const handleCheck = async () => {
    try {
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const handleSaveHubAndRecheck = async () => {
    const raw = hubForm.getFieldsValue()
    const username = (raw.username || '').trim()
    const secret = (raw.secret || '').trim()
    if (!username || !secret) {
      message.warning(t.home.hubRequiredMsg)
      return
    }
    setHubSubmitting(true)
    try {
      await api.saveRegCred({ registry: 'docker.io', username, secret })
      qc.invalidateQueries({ queryKey: ['regCreds'] })
      setHubModalOpen(false)
      hubForm.setFieldValue('secret', '')
      message.success(t.home.hubSavedRechecking)
      const p = await api.triggerCheck(hostFilter || undefined, true)
      setLiveProgress(p)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setHubSubmitting(false)
    }
  }

  const formatLastChecked = (ts?: number): string => {
    if (!ts) return t.home.neverChecked
    const baseSec = overview.dataUpdatedAt
      ? Math.max(nowSec, Math.floor(overview.dataUpdatedAt / 1000))
      : nowSec
    const diffSec = Math.max(0, baseSec - ts)
    if (diffSec < 60) {
      return `${t.home.lastChecked}${t.home.justNow}`
    }
    if (diffSec < 3600) {
      return `${t.home.lastChecked}${t.home.minsAgo.replace('{m}', String(Math.floor(diffSec / 60)))}`
    }
    if (diffSec < 86400) {
      return `${t.home.lastChecked}${t.home.hoursAgo.replace('{h}', String(Math.floor(diffSec / 3600)))}`
    }
    return `${t.home.lastChecked}${new Date(ts * 1000).toLocaleString(locale)}`
  }

  const renderHeaderSubtitle = (): string => {
    if (isRunning) {
      if (progress?.phase === 'discovering') {
        return progress.hostName
          ? `${t.home.discoveringHost}${progress.hostName}`
          : t.home.discoveringGeneric
      }
      if ((progress?.total ?? 0) > 0) {
        return t.home.checkingProgressWithCount
          .replace('{completed}', String(progress?.completed ?? 0))
          .replace('{total}', String(progress?.total ?? 0))
      }
      return `${t.home.checkingProgress}…`
    }

    const total = overview.data?.totalAppCount ?? 0
    const upToDate = overview.data?.upToDateCount ?? 0
    const timePart = formatLastChecked(overview.data?.lastCheckedAt)
    if (total > 0) {
      return `${timePart} · ${t.home.summaryCounts
        .replace('{total}', String(total))
        .replace('{upToDate}', String(upToDate))}`
    }
    return timePart
  }

  const hosts = overview.data?.hosts ?? []
  const rawFailed = overview.data?.failed

  const rateLimitedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind === 'rate_limited'),
    [rawFailed],
  )
  const otherFailedItems = useMemo(
    () => (rawFailed ?? []).filter((f) => f.errorKind !== 'rate_limited'),
    [rawFailed],
  )

  const formatFailedItemLine = (f: FailedItem) => {
    const displayName = f.serviceName && f.serviceName !== 'main' ? f.serviceName : f.appName
    const hostPart = t.home.failedHostWrap.replace('{host}', f.hostName)
    if (f.errorKind === 'auth_required') {
      return (
        <span>
          <Typography.Text strong>{displayName}</Typography.Text>
          {hostPart}
          {t.home.failedAuthReason}
        </span>
      )
    }
    return (
      <span>
        <Typography.Text strong>{displayName}</Typography.Text>
        {hostPart}— {f.errorMsg}
      </span>
    )
  }

  const renderGrid = (items: AppCardItem[], emptyText: string) => {
    if (items.length === 0) {
      return (
        <Card
          size="small"
          style={{
            borderStyle: 'dashed',
            textAlign: 'center',
            background: 'transparent',
          }}
        >
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {isRunning ? t.home.checkingGroupEmpty : emptyText}
          </Typography.Text>
        </Card>
      )
    }
    return (
      <Row gutter={[16, 16]} align="stretch">
        {items.map((item) => (
          <Col
            key={`${item.group}-${item.key}`}
            xs={24}
            md={12}
            lg={8}
            style={{ display: 'flex' }}
          >
            <div style={{ width: '100%' }}>
              <AppCard
                app={item}
                isChecking={isCardChecking(item)}
                onChanged={() => qc.invalidateQueries({ queryKey: ['overview'] })}
              />
            </div>
          </Col>
        ))}
      </Row>
    )
  }

  if (!overview.isPending && hosts.length === 0) {
    return (
      <Card style={{ textAlign: 'center', padding: '48px 16px' }}>
        <Empty description={t.home.emptyNoHosts}>
          <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/hosts')}>
            {t.home.emptyNoHostsBtn}
          </Button>
        </Empty>
      </Card>
    )
  }

  const totalLoadedCards = normalList.length + cautionList.length + noUpdateList.length
  const isInitialScanning = isRunning && totalLoadedCards === 0
  const showCautionGroup = cautionList.length > 0 || !isRunning

  const activeGroupKeys: string[] = []
  if (normalExpanded) activeGroupKeys.push('normal')
  if (showCautionGroup && cautionExpanded) activeGroupKeys.push('caution')
  if (noUpdateExpanded) activeGroupKeys.push('noUpdate')

  const handleCollapseChange = (keys: string | string[]) => {
    const keySet = new Set(Array.isArray(keys) ? keys : [keys])
    setNormalExpanded(keySet.has('normal'))
    if (showCautionGroup) {
      setCautionToggled(keySet.has('caution'))
    }
    setNoUpdateExpanded(keySet.has('noUpdate'))
  }

  const collapseItems = [
    {
      key: 'normal',
      label: (
        <Space size={8} align="center">
          <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
            {t.home.groupNormal}
          </Typography.Text>
          <Badge count={normalList.length} showZero color={token.colorPrimary} />
        </Space>
      ),
      children: renderGrid(normalList, t.home.emptyNormal),
    },
    ...(showCautionGroup
      ? [
          {
            key: 'caution',
            label: (
              <Space size={8} align="center">
                <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
                  {t.home.groupCaution}
                </Typography.Text>
                <Badge count={cautionList.length} showZero color={token.colorWarning} />
              </Space>
            ),
            children: renderGrid(cautionList, t.home.emptyCaution),
          },
        ]
      : []),
    {
      key: 'noUpdate',
      label: (
        <Space size={8} align="center">
          <Typography.Text strong style={{ fontSize: token.fontSizeLG }}>
            {t.home.groupNoUpdate}
          </Typography.Text>
          <Badge
            count={noUpdateList.length}
            showZero
            style={{
              backgroundColor: token.colorFillSecondary,
              color: token.colorText,
              boxShadow: 'none',
            }}
          />
        </Space>
      ),
      children: renderGrid(noUpdateList, t.home.emptyNoUpdate),
    },
  ]

  return (
    <Flex vertical gap={16} style={{ width: '100%' }}>
      {/* 顶栏标题与检查按钮 */}
      <Flex justify="space-between" align="center" wrap gap={12}>
        <div>
          <Typography.Title level={3} style={{ margin: 0 }}>
            {t.home.title}
          </Typography.Title>
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {renderHeaderSubtitle()}
          </Typography.Text>
        </div>

        <Button
          type="primary"
          icon={<SyncOutlined spin={isRunning} />}
          disabled={isRunning}
          onClick={handleCheck}
        >
          {isRunning ? t.home.checking : t.home.checkNow}
        </Button>
      </Flex>

      {/* 中小屏（< 1980px）全宽内嵌更新任务队列：紧接标题栏下方，0 横向挤压、0 遮挡卡片 */}
      {!isUltrawide && queueJobs.length > 0 && !queueCollapsed && (
        <UpdateQueuePanel mode="inline" />
      )}

      {/* 多设备筛选 */}
      {hosts.length > 1 && (
        <div>
          <Segmented
            value={hostFilter}
            onChange={(v) => setHostFilter(String(v))}
            options={[
              { label: t.home.allHosts, value: '' },
              ...hosts.map((h) => ({ label: h.name, value: h.id })),
            ]}
          />
        </div>
      )}

      {/* 情况 A：Docker Hub 100 次/6小时未登录限流专用提醒条 */}
      {rateLimitedItems.length > 0 && (
        <Alert
          type="warning"
          showIcon
          title={
            <Space size={8} wrap>
              <Tag color="warning" style={{ margin: 0 }}>
                {t.home.rateLimitBadge}
              </Tag>
              <Typography.Text strong>{t.home.rateLimitTitle}</Typography.Text>
            </Space>
          }
          description={t.home.rateLimitDesc}
          action={
            <Button type="primary" size="small" onClick={() => setHubModalOpen(true)}>
              {t.home.rateLimitBtn}
            </Button>
          }
        />
      )}

      {/* 情况 B：私有仓库未授权或其他异常提示条 */}
      {otherFailedItems.length > 0 && (
        <Alert
          type="warning"
          showIcon
          title={
            <Space size={8} wrap>
              <Typography.Text strong>
                {otherFailedItems.length === 1
                  ? t.home.failedBannerSingle
                  : t.home.failedBannerMulti.replace('{count}', String(otherFailedItems.length))}
              </Typography.Text>
              <Typography.Text>{formatFailedItemLine(otherFailedItems[0])}</Typography.Text>
              {otherFailedItems.length > 1 && (
                <Button
                  type="link"
                  size="small"
                  style={{ padding: 0, height: 'auto' }}
                  onClick={() => setFailedExpanded((v) => !v)}
                >
                  {failedExpanded ? t.home.failedCollapse : t.home.failedExpandAll}
                </Button>
              )}
            </Space>
          }
          description={
            failedExpanded && otherFailedItems.length > 1 ? (
              <Space direction="vertical" size={2} style={{ marginTop: 4 }}>
                {otherFailedItems.slice(1).map((f) => (
                  <Typography.Text key={f.serviceKey} style={{ fontSize: token.fontSizeSM }}>
                    • {formatFailedItemLine(f)}
                  </Typography.Text>
                ))}
              </Space>
            ) : undefined
          }
          action={
            <Button type="link" size="small" onClick={() => navigate('/settings')}>
              {t.home.failedGoCreds}
            </Button>
          }
        />
      )}

      {/* 首次尚无应用数据且正在扫描时，不渲染虚假 0 空结论 */}
      {isInitialScanning ? (
        <Card style={{ textAlign: 'center', padding: '36px 16px' }}>
          <Space direction="vertical" size="middle" align="center">
            <Spin />
            <Typography.Text type="secondary">{renderHeaderSubtitle()}</Typography.Text>
          </Space>
        </Card>
      ) : (
        <Collapse
          ghost
          activeKey={activeGroupKeys}
          onChange={handleCollapseChange}
          style={{ width: '100%' }}
          styles={{
            header: { paddingInline: 0, alignItems: 'center' },
            body: { paddingInline: 0, paddingTop: 4, paddingBottom: 16 },
          }}
          items={collapseItems}
        />
      )}

      {/* 一键登录 Docker Hub 账号弹窗 */}
      <Modal
        open={hubModalOpen}
        title={t.home.hubModalTitle}
        onCancel={() => setHubModalOpen(false)}
        okText={t.home.hubSaveAndRecheck}
        cancelText={t.ignoreModal.cancel}
        confirmLoading={hubSubmitting}
        onOk={handleSaveHubAndRecheck}
        width={440}
        destroyOnHidden
      >
        <Form form={hubForm} layout="vertical" style={{ paddingTop: 8 }}>
          <Form.Item name="username" label={t.home.hubUserLabel} required>
            <Input placeholder={t.home.hubUserPlaceholder} autoFocus />
          </Form.Item>
          <Form.Item
            name="secret"
            label={t.home.hubSecretLabel}
            required
            style={{ marginBottom: 0 }}
          >
            <Input.Password
              placeholder={t.home.hubSecretPlaceholder}
              onPressEnter={handleSaveHubAndRecheck}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Flex>
  )
}
