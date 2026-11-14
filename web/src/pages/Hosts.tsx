import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Empty,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Segmented,
  Space,
  Tag,
  Timeline,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudServerOutlined,
  CodeOutlined,
  CopyOutlined,
  DesktopOutlined,
  DisconnectOutlined,
  EditOutlined,
  HddOutlined,
  KeyOutlined,
  PlusOutlined,
  ReloadOutlined,
  SyncOutlined,
  WarningOutlined,
  WifiOutlined,
} from '@ant-design/icons'
import { useSearchParams } from 'react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  ApiError,
  showApiError,
  type DiskStat,
  type HostInfo,
  type HostInput,
  type HostItem,
  type HostRuntimeStat,
  type HostTestResult,
} from '../api/client'
import { useI18n } from '../i18n'

type PlatformId = 'synology' | 'fnos' | 'openwrt' | 'linux' | 'local'

interface PlatformSpec {
  id: PlatformId
  kind: 'synology' | 'fnos' | 'openwrt' | 'docker'
  transports: ('dsm_api' | 'ssh' | 'local')[]
  defaultTransport: 'dsm_api' | 'ssh' | 'local'
}

const PLATFORM_SPECS: PlatformSpec[] = [
  {
    id: 'synology',
    kind: 'synology',
    transports: ['dsm_api', 'ssh'],
    defaultTransport: 'dsm_api',
  },
  {
    id: 'fnos',
    kind: 'fnos',
    transports: ['ssh'],
    defaultTransport: 'ssh',
  },
  {
    id: 'openwrt',
    kind: 'openwrt',
    transports: ['ssh'],
    defaultTransport: 'ssh',
  },
  {
    id: 'linux',
    kind: 'docker',
    transports: ['ssh'],
    defaultTransport: 'ssh',
  },
  {
    id: 'local',
    kind: 'docker',
    transports: ['local'],
    defaultTransport: 'local',
  },
]

export default function HostsPage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()
  const authQuery = useQuery({ queryKey: ['authStatus'], queryFn: api.authStatus })
  const hostsQuery = useQuery({ queryKey: ['hosts', locale], queryFn: api.listHosts })

  // 本地已收录快照（纯 SQLite 读取，首次进入毫秒级直出）
  const cachedMetricsQuery = useQuery({
    queryKey: ['host-metrics-cached'],
    queryFn: () => api.listHostMetrics({ cached: true }),
    staleTime: 30000,
  })

  // 实时设备资源轮询（对齐容器列表页，每 10 秒刷新并回写快照）
  const liveMetricsQuery = useQuery({
    queryKey: ['host-metrics'],
    queryFn: () => api.listHostMetrics(),
    refetchInterval: 10000,
  })

  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<HostItem | null>(null)
  const [platformId, setPlatformId] = useState<PlatformId>('synology')
  const [form] = Form.useForm<HostInput>()
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<HostTestResult | null>(null)
  const [generatedPubKey, setGeneratedPubKey] = useState('')
  const [saving, setSaving] = useState(false)

  // 2FA 即时弹窗状态（仅在测试连接或保存返回 needOtp 时弹出）
  const [otpModalOpen, setOtpModalOpen] = useState(false)
  const [otpCode, setOtpCode] = useState('')
  const [otpTrigger, setOtpTrigger] = useState<'test' | 'save'>('save')

  const canAddLocal = Boolean(hostsQuery.data?.canAddLocal)

  const visiblePlatforms = useMemo(() => {
    return PLATFORM_SPECS.filter((p) => {
      if (p.id === 'local') {
        if (editing) return editing.transport === 'local'
        return canAddLocal
      }
      if (editing && editing.transport === 'local') {
        return false
      }
      return true
    })
  }, [canAddLocal, editing])

  const selectPlatform = (nextId: PlatformId) => {
    const spec = PLATFORM_SPECS.find((p) => p.id === nextId) ?? PLATFORM_SPECS[0]
    setPlatformId(spec.id)
    setTestResult(null)
    const curUser = String(form.getFieldValue('sshUser') || '').trim()
    form.setFieldsValue({
      kind: spec.kind,
      transport: spec.defaultTransport,
      ...(spec.id === 'openwrt' && !curUser ? { sshUser: 'root' } : {}),
    })
  }

  const openAdd = () => {
    setEditing(null)
    setTestResult(null)
    setGeneratedPubKey('')
    setOtpModalOpen(false)
    setOtpCode('')
    form.resetFields()
    setPlatformId('synology')
    form.setFieldsValue({
      name: '',
      kind: 'synology',
      transport: 'dsm_api',
      port: 22,
      sshAuth: 'password',
    })
    setModalOpen(true)
  }

  useEffect(() => {
    if (searchParams.get('connectLocal') === '1' && authQuery.data && hostsQuery.data) {
      setEditing(null)
      setTestResult(null)
      setGeneratedPubKey('')
      setOtpModalOpen(false)
      setOtpCode('')
      form.resetFields()
      const instMode = authQuery.data.installMode
      if (instMode === 'synology_spk') {
        setPlatformId('synology')
        form.setFieldsValue({
          name: t.auth.spkDefaultName,
          kind: 'synology',
          transport: 'dsm_api',
          dsmUrl: 'http://127.0.0.1:5000',
          address: '127.0.0.1',
          port: 22,
          sshAuth: 'password',
        })
      } else if (canAddLocal) {
        setPlatformId('local')
        form.setFieldsValue({
          name:
            instMode === 'fnos_fpk'
              ? t.hosts.fnosLocalDefaultName
              : instMode === 'openwrt'
                ? t.hosts.openwrtLocalDefaultName
                : t.hosts.localDefaultName,
          kind:
            instMode === 'fnos_fpk'
              ? 'fnos'
              : instMode === 'openwrt'
                ? 'openwrt'
                : 'docker',
          transport: 'local',
        })
      } else if (instMode === 'fnos_fpk') {
        setPlatformId('fnos')
        form.setFieldsValue({
          name: t.hosts.fnosLocalDefaultName,
          kind: 'fnos',
          transport: 'ssh',
          address: '127.0.0.1',
          port: 22,
          sshAuth: 'password',
        })
      } else if (instMode === 'openwrt') {
        setPlatformId('openwrt')
        form.setFieldsValue({
          name: t.hosts.openwrtLocalDefaultName,
          kind: 'openwrt',
          transport: 'ssh',
          address: '127.0.0.1',
          port: 22,
          sshUser: 'root',
          sshAuth: 'password',
        })
      } else {
        setPlatformId('linux')
        form.setFieldsValue({
          name: t.hosts.localDefaultName,
          kind: 'docker',
          transport: 'ssh',
          address: '127.0.0.1',
          port: 22,
          sshAuth: 'password',
        })
      }
      setModalOpen(true)
      const next = new URLSearchParams(searchParams)
      next.delete('connectLocal')
      setSearchParams(next, { replace: true })
    }
  }, [
    authQuery.data,
    canAddLocal,
    form,
    hostsQuery.data,
    searchParams,
    setSearchParams,
    t.auth.spkDefaultName,
    t.hosts.fnosLocalDefaultName,
    t.hosts.localDefaultName,
    t.hosts.openwrtLocalDefaultName,
  ])

  const openEdit = (h: HostItem) => {
    setEditing(h)
    setTestResult(null)
    setGeneratedPubKey('')
    setOtpModalOpen(false)
    setOtpCode('')
    form.resetFields()
    const initPlatform: PlatformId =
      h.transport === 'local'
        ? 'local'
        : h.kind === 'synology'
          ? 'synology'
          : h.kind === 'fnos'
            ? 'fnos'
            : h.kind === 'openwrt'
              ? 'openwrt'
              : 'linux'
    setPlatformId(initPlatform)
    form.setFieldsValue({
      name: h.name,
      kind: h.kind,
      transport: h.transport,
      address: h.address,
      port: h.port || 22,
      sshUser: h.sshUser,
      sshAuth: h.sshAuth || 'password',
      dsmUrl: h.dsmUrl,
      dsmUser: h.dsmUser,
    })
    setModalOpen(true)
  }

  const runTest = async (extraOtp?: string) => {
    const vals = { ...(form.getFieldsValue(true) as HostInput) }
    if (extraOtp?.trim()) {
      vals.dsmOtpCode = extraOtp.trim()
    }
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api.testHost(editing ? { ...vals, hostId: editing.id } : vals)
      setTestResult(res)
      if (res.needOtp) {
        setOtpTrigger('test')
        setOtpCode('')
        setOtpModalOpen(true)
      } else {
        setOtpModalOpen(false)
      }
    } catch (e) {
      if (e instanceof ApiError && (e.code === 'need_otp' || e.testResult?.needOtp)) {
        if (e.testResult) setTestResult(e.testResult)
        setOtpTrigger('test')
        setOtpCode('')
        setOtpModalOpen(true)
        return
      }
      showApiError(e)
    } finally {
      setTesting(false)
    }
  }

  const handleGenerateKey = async () => {
    try {
      const kp = await api.keygen()
      form.setFieldsValue({ sshAuth: 'key', sshSecret: kp.privateKey })
      setGeneratedPubKey(kp.authorizedKey)
    } catch (e) {
      showApiError(e)
    }
  }

  const handleSave = async (extraOtp?: string) => {
    try {
      await form.validateFields()
    } catch {
      return
    }
    const vals = { ...(form.getFieldsValue(true) as HostInput) }
    if (extraOtp?.trim()) {
      vals.dsmOtpCode = extraOtp.trim()
    }
    if (testResult?.deviceTokenNonce) {
      vals.deviceTokenNonce = testResult.deviceTokenNonce
    }
    delete vals.hostId
    setSaving(true)
    try {
      if (editing) {
        await api.updateHost(editing.id, vals)
      } else {
        await api.createHost(vals)
      }
      setOtpModalOpen(false)
      setOtpCode('')
      message.success(t.hosts.saved)
      setModalOpen(false)
      qc.invalidateQueries({ queryKey: ['authStatus'] })
      qc.invalidateQueries({ queryKey: ['hosts'] })
      qc.invalidateQueries({ queryKey: ['host-metrics'] })
      qc.invalidateQueries({ queryKey: ['host-metrics-cached'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
      qc.invalidateQueries({ queryKey: ['containers'] })
    } catch (e) {
      if (e instanceof ApiError && e.testResult) {
        setTestResult(e.testResult)
      }
      if (e instanceof ApiError && (e.code === 'need_otp' || e.testResult?.needOtp)) {
        setOtpTrigger('save')
        setOtpCode('')
        setOtpModalOpen(true)
        return
      }
      showApiError(e)
    } finally {
      setSaving(false)
    }
  }

  const onConfirmOtpModal = () => {
    if (otpTrigger === 'test') {
      runTest(otpCode)
    } else {
      handleSave(otpCode)
    }
  }

  const hosts = hostsQuery.data?.items ?? []

  const { metricsByHostId, isShowingCached } = useMemo(() => {
    const liveItems = liveMetricsQuery.data?.items
    const cachedItems = cachedMetricsQuery.data?.items
    const map = new Map<string, HostRuntimeStat>()
    if (liveItems) {
      for (const m of liveItems) map.set(m.hostId, m)
      return { metricsByHostId: map, isShowingCached: false }
    }
    if (cachedItems && cachedItems.some((m) => m.online)) {
      for (const m of cachedItems) map.set(m.hostId, m)
      return { metricsByHostId: map, isShowingCached: true }
    }
    return { metricsByHostId: map, isShowingCached: false }
  }, [cachedMetricsQuery.data?.items, liveMetricsQuery.data?.items])

  const liveOnlineByHostId = useMemo(() => {
    const map = new Map<string, boolean>()
    for (const m of liveMetricsQuery.data?.items ?? []) {
      map.set(m.hostId, m.online)
    }
    return map
  }, [liveMetricsQuery.data?.items])

  const handleRefresh = () => {
    void hostsQuery.refetch()
    void liveMetricsQuery.refetch()
  }

  const isRefreshing = hostsQuery.isFetching || liveMetricsQuery.isFetching

  const formatCap = (bytes: number): string => {
    if (!bytes || bytes <= 0) return '0 GB'
    const tb = bytes / 1024 ** 4
    if (tb >= 1) return `${tb.toFixed(2)} TB`
    const gb = bytes / 1024 ** 3
    if (gb >= 100) return `${gb.toFixed(0)} GB`
    if (gb >= 1) return `${gb.toFixed(1)} GB`
    const mb = bytes / (1024 * 1024)
    return `${Math.max(1, Math.round(mb))} MB`
  }

  const formatMemCompact = (usedBytes: number, totalBytes: number): string => {
    if (!totalBytes || totalBytes <= 0) return '—'
    const gbTotal = totalBytes / 1024 ** 3
    if (gbTotal >= 1) {
      const usedStr = (usedBytes / 1024 ** 3).toFixed(1)
      const totalStr = gbTotal >= 10 ? gbTotal.toFixed(0) : gbTotal.toFixed(1).replace(/\.0$/, '')
      return `${usedStr} / ${totalStr} GB`
    }
    const mbUsed = Math.round(usedBytes / (1024 * 1024))
    const mbTotal = Math.round(totalBytes / (1024 * 1024))
    return `${mbUsed} / ${mbTotal} MB`
  }

  const formatDiskTitle = (d: DiskStat): string => {
    const m = d.mount.match(/^\/(?:volume|vol)(\d+)$/i)
    if (m) {
      const volTitle = t.hosts.metricVolumeLabel.replace('{n}', m[1])
      return d.label ? `${volTitle} (${d.mount} · ${d.label})` : `${volTitle} (${d.mount})`
    }
    return d.label ? `${d.label} (${d.mount})` : d.mount
  }

  const getMetricColor = (pct: number) => {
    if (pct >= 90) return token.colorError
    if (pct >= 75) return token.colorWarning
    return token.colorPrimary
  }

  const getMetricValueColor = (pct: number) => {
    if (pct >= 90) return token.colorError
    if (pct >= 75) return token.colorWarning
    return token.colorText
  }
  const transportVal = Form.useWatch('transport', form)
  const authVal = Form.useWatch('sshAuth', form)
  const currentSpec = PLATFORM_SPECS.find((p) => p.id === platformId) ?? PLATFORM_SPECS[0]

  const effectiveTransport = transportVal || editing?.transport || currentSpec.defaultTransport
  const effectiveSshAuth = authVal || editing?.sshAuth || 'password'
  const canKeepDsmSecret = Boolean(editing && editing.transport === 'dsm_api')
  const canKeepSshPassword = Boolean(
    editing && editing.transport === 'ssh' && (editing.sshAuth || 'password') === 'password',
  )
  const canKeepSshKey = Boolean(
    editing && editing.transport === 'ssh' && editing.sshAuth === 'key' && !generatedPubKey,
  )
  const canKeepCurrentCred =
    effectiveTransport === 'dsm_api'
      ? canKeepDsmSecret
      : effectiveTransport === 'ssh'
        ? effectiveSshAuth === 'key'
          ? canKeepSshKey
          : canKeepSshPassword
        : false

  const formatCpuArch = (info?: HostInfo) => {
    const arch = (info?.arch || '').toLowerCase().trim()
    const variant = (info?.variant || '').toLowerCase().trim()
    if (!arch) return t.hosts.archUnknown
    if (arch === 'amd64' || arch === 'x86_64') return 'x86_64 (amd64)'
    if (arch === 'arm64' || arch === 'aarch64') return 'ARM64 (arm64)'
    if (arch === 'arm') {
      return variant ? `${t.hosts.archArm32} (arm/${variant})` : `${t.hosts.archArm32} (arm)`
    }
    if (arch === '386' || arch === 'i386' || arch === 'x86') {
      return `${t.hosts.archX8632} (386)`
    }
    return variant ? `${arch}/${variant}` : arch
  }

  const formatSystemEnv = (info?: HostInfo) => {
    if (!info) return '—'
    const osPart = (info.platformVersion || info.os || '').trim()
    const dk = (info.dockerVersion || '').trim()
    const dockerPart =
      dk && !dk.includes('DSM WebAPI')
        ? /^(docker|container)/i.test(dk)
          ? dk
          : `Docker ${dk}`
        : ''
    return [osPart, dockerPart].filter(Boolean).join(' · ') || '—'
  }

  const getHostCardMeta = (h: HostItem) => {
    const transportTitle =
      h.transport === 'local'
        ? t.hosts.localSocket
        : h.transport === 'dsm_api'
          ? t.hosts.dsmWebApi
          : t.hosts.sshRemote

    if (h.kind === 'synology') {
      return {
        icon: <HddOutlined style={{ fontSize: 18, color: token.colorPrimary }} />,
        iconBg: token.colorPrimaryBg,
        subtitle: `${t.hosts.platformSynology} · ${transportTitle}`,
      }
    }
    if (h.kind === 'fnos') {
      return {
        icon: <HddOutlined style={{ fontSize: 18, color: '#13c2c2' }} />,
        iconBg: 'rgba(19, 194, 194, 0.12)',
        subtitle: `${t.hosts.platformFnos} · ${transportTitle}`,
      }
    }
    if (h.kind === 'openwrt') {
      const openwrtName = h.info?.platformVersion?.toLowerCase().includes('istoreos')
        ? t.hosts.tagIstoreos
        : t.hosts.tagOpenwrt
      return {
        icon: <WifiOutlined style={{ fontSize: 18, color: '#fa8c16' }} />,
        iconBg: 'rgba(250, 140, 22, 0.12)',
        subtitle: `${openwrtName} · ${transportTitle}`,
      }
    }
    if (h.transport === 'local') {
      return {
        icon: <DesktopOutlined style={{ fontSize: 18, color: token.colorPrimary }} />,
        iconBg: token.colorPrimaryBg,
        subtitle: `${t.hosts.localDefaultName} · ${transportTitle}`,
      }
    }
    return {
      icon: <CodeOutlined style={{ fontSize: 18, color: '#6366f1' }} />,
      iconBg: 'rgba(99, 102, 241, 0.12)',
      subtitle: `${t.hosts.platformLinux} · ${transportTitle}`,
    }
  }

  const platformMeta = (id: PlatformId) => {
    switch (id) {
      case 'synology':
        return {
          icon: <HddOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformSynology,
          desc: t.hosts.platformSynologyDesc,
        }
      case 'fnos':
        return {
          icon: <HddOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformFnos,
          desc: t.hosts.platformFnosDesc,
        }
      case 'openwrt':
        return {
          icon: <WifiOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformOpenwrt,
          desc: t.hosts.platformOpenwrtDesc,
        }
      case 'linux':
        return {
          icon: <CloudServerOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformLinux,
          desc: t.hosts.platformLinuxDesc,
        }
      case 'local':
        return {
          icon: <DesktopOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformLocal,
          desc: t.hosts.platformLocalDesc,
        }
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center" wrap gap={12}>
        <Space size={10} align="center" wrap>
          <Typography.Title level={3} style={{ margin: 0 }}>
            {t.hosts.title}
          </Typography.Title>
          {hosts.length > 0 && isShowingCached && isRefreshing && (
            <Tag icon={<SyncOutlined spin />} color="processing" style={{ margin: 0 }}>
              {t.hosts.syncingLiveBadge}
            </Tag>
          )}
        </Space>
        <Space wrap>
          {hosts.length > 0 && (
            <Button icon={<ReloadOutlined spin={isRefreshing} />} onClick={handleRefresh}>
              {t.hosts.refresh}
            </Button>
          )}
          <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
            {t.hosts.addHost}
          </Button>
        </Space>
      </Flex>

      {hosts.length === 0 ? (
        <Card style={{ textAlign: 'center', padding: '32px 16px' }}>
          <Empty description={t.hosts.empty} />
        </Card>
      ) : (
        <Row gutter={[16, 16]}>
          {hosts.map((h) => {
            const cardMeta = getHostCardMeta(h)
            const liveOnline = liveOnlineByHostId.get(h.id)
            const effectiveStatus =
              liveOnline !== undefined ? (liveOnline ? 'online' : 'offline') : h.status
            const statusStyle =
              effectiveStatus === 'online'
                ? {
                    fg: token.colorSuccess,
                    bg: token.colorSuccessBg,
                    dot: token.colorSuccess,
                    label: t.hosts.online,
                  }
                : effectiveStatus === 'offline'
                  ? {
                      fg: token.colorError,
                      bg: token.colorErrorBg,
                      dot: token.colorError,
                      label: t.hosts.offline,
                    }
                  : {
                      fg: token.colorTextSecondary,
                      bg: token.colorFillSecondary,
                      dot: token.colorTextQuaternary,
                      label: t.hosts.unknown,
                    }
            const cleanDsmAddr = (h.dsmUrl || '').replace(/^https?:\/\//i, '').replace(/\/+$/, '')
            const sysEnvText = formatSystemEnv(h.info)
            const metric = metricsByHostId.get(h.id)
            const hasLiveMetrics = Boolean(
              effectiveStatus === 'online' && metric && metric.online && metric.memTotalBytes > 0,
            )
            const cpuPct = hasLiveMetrics
              ? Math.max(0, Math.min(100, metric!.cpuPercent ?? 0))
              : 0
            const memPct =
              hasLiveMetrics && metric!.memTotalBytes > 0
                ? Math.max(
                    0,
                    Math.min(100, Math.round((metric!.memUsedBytes / metric!.memTotalBytes) * 100)),
                  )
                : 0
            const disks = hasLiveMetrics ? (metric!.disks ?? []) : []
            const totalDiskBytes = disks.reduce((s, d) => s + d.totalBytes, 0)
            const totalFreeBytes = disks.reduce((s, d) => s + d.freeBytes, 0)

            return (
              <Col key={h.id} xs={24} md={12}>
                <Card
                  styles={{ body: { padding: 18 } }}
                  actions={[
                    <Button
                      key="edit"
                      type="text"
                      size="small"
                      icon={<EditOutlined />}
                      onClick={() => openEdit(h)}
                    >
                      {t.hosts.editHost}
                    </Button>,
                    <Popconfirm
                      key="del"
                      title={t.hosts.deleteConfirm}
                      description={t.hosts.deleteConfirmDesc}
                      okText={t.hosts.unbindOkBtn}
                      cancelText={t.ignoreModal.cancel}
                      okButtonProps={{ danger: true }}
                      onConfirm={async () => {
                        try {
                          await api.deleteHost(h.id)
                          message.success(t.hosts.deleted)
                          qc.invalidateQueries({ queryKey: ['hosts'] })
                          qc.invalidateQueries({ queryKey: ['host-metrics'] })
                          qc.invalidateQueries({ queryKey: ['host-metrics-cached'] })
                          qc.invalidateQueries({ queryKey: ['overview'] })
                          qc.invalidateQueries({ queryKey: ['containers'] })
                        } catch (e) {
                          showApiError(e)
                        }
                      }}
                    >
                      <Button type="text" danger size="small" icon={<DisconnectOutlined />}>
                        {t.hosts.unbindHost}
                      </Button>
                    </Popconfirm>,
                  ]}
                >
                  <Space direction="vertical" size={14} style={{ width: '100%' }}>
                    <Flex justify="space-between" align="flex-start" gap={12}>
                      <Flex align="center" gap={12} style={{ minWidth: 0, flex: 1 }}>
                        <div
                          style={{
                            width: 38,
                            height: 38,
                            borderRadius: token.borderRadiusLG,
                            background: cardMeta.iconBg,
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            flexShrink: 0,
                          }}
                        >
                          {cardMeta.icon}
                        </div>
                        <div style={{ minWidth: 0, flex: 1 }}>
                          <Typography.Text
                            strong
                            ellipsis={{ tooltip: h.name }}
                            style={{ display: 'block', fontSize: 15, lineHeight: 1.35 }}
                          >
                            {h.name}
                          </Typography.Text>
                          <Typography.Text
                            type="secondary"
                            ellipsis={{ tooltip: cardMeta.subtitle }}
                            style={{ display: 'block', fontSize: token.fontSizeSM, marginTop: 2 }}
                          >
                            {cardMeta.subtitle}
                          </Typography.Text>
                        </div>
                      </Flex>

                      <span
                        style={{
                          display: 'inline-flex',
                          alignItems: 'center',
                          gap: 6,
                          padding: '2px 10px',
                          borderRadius: 999,
                          fontSize: token.fontSizeSM,
                          fontWeight: 500,
                          color: statusStyle.fg,
                          background: statusStyle.bg,
                          flexShrink: 0,
                        }}
                      >
                        <span
                          style={{
                            width: 6,
                            height: 6,
                            borderRadius: '50%',
                            background: statusStyle.dot,
                          }}
                        />
                        {statusStyle.label}
                      </span>
                    </Flex>

                    <div
                      style={{
                        background: token.colorFillAlter,
                        borderRadius: token.borderRadiusLG,
                        padding: '10px 14px',
                        display: 'flex',
                        flexDirection: 'column',
                        gap: 6,
                        fontSize: token.fontSizeSM,
                      }}
                    >
                      <Flex justify="space-between" align="center" gap={12}>
                        <Typography.Text
                          type="secondary"
                          style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                        >
                          {t.hosts.cardConnAddr}
                        </Typography.Text>
                        <Typography.Text
                          ellipsis={{
                            tooltip:
                              h.transport === 'dsm_api'
                                ? `${h.dsmUrl || ''} (${h.dsmUser || ''})`
                                : h.transport === 'ssh'
                                  ? `${h.address}:${h.port || 22} (${h.sshUser || ''})`
                                  : '/var/run/docker.sock',
                          }}
                          style={{ fontSize: token.fontSizeSM, fontFamily: token.fontFamilyCode }}
                        >
                          {h.transport === 'dsm_api' ? (
                            <>
                              {cleanDsmAddr}
                              {h.dsmUser && (
                                <span
                                  style={{
                                    color: token.colorTextSecondary,
                                    fontFamily: token.fontFamily,
                                    marginLeft: 4,
                                  }}
                                >
                                  ({h.dsmUser})
                                </span>
                              )}
                            </>
                          ) : h.transport === 'ssh' ? (
                            <>
                              {h.address}:{h.port || 22}
                              {h.sshUser && (
                                <span
                                  style={{
                                    color: token.colorTextSecondary,
                                    fontFamily: token.fontFamily,
                                    marginLeft: 4,
                                  }}
                                >
                                  ({h.sshUser})
                                </span>
                              )}
                            </>
                          ) : (
                            '/var/run/docker.sock'
                          )}
                        </Typography.Text>
                      </Flex>

                      <Flex justify="space-between" align="center" gap={12}>
                        <Typography.Text
                          type="secondary"
                          style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                        >
                          {t.hosts.cardCpuArch}
                        </Typography.Text>
                        <Typography.Text
                          strong
                          style={{ fontSize: token.fontSizeSM, fontFamily: token.fontFamilyCode }}
                        >
                          {formatCpuArch(h.info)}
                        </Typography.Text>
                      </Flex>

                      <Flex justify="space-between" align="center" gap={12}>
                        <Typography.Text
                          type="secondary"
                          style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                        >
                          {t.hosts.cardSysEnv}
                        </Typography.Text>
                        <Typography.Text
                          ellipsis={{ tooltip: sysEnvText !== '—' ? sysEnvText : undefined }}
                          style={{ fontSize: token.fontSizeSM }}
                        >
                          {sysEnvText}
                        </Typography.Text>
                      </Flex>
                    </div>

                    {/* 实时系统资源区（CPU / 内存 / 多磁盘同行分段剩余空间；离线或待采集时保持完全同构等高骨架） */}
                    <div
                      style={{
                        border: `1px solid ${token.colorBorderSecondary}`,
                        background: token.colorFillQuaternary,
                        borderRadius: token.borderRadiusLG,
                        padding: '10px 14px',
                        display: 'flex',
                        flexDirection: 'column',
                        gap: 10,
                        opacity: hasLiveMetrics ? 1 : 0.6,
                      }}
                    >
                      <Row gutter={14}>
                        <Col span={12}>
                          <Tooltip
                            title={
                              hasLiveMetrics
                                ? t.hosts.metricCpuTip.replace('{pct}', cpuPct.toFixed(1))
                                : undefined
                            }
                          >
                            <div>
                              <Flex justify="space-between" align="center" gap={6} style={{ marginBottom: 5 }}>
                                <Typography.Text
                                  type="secondary"
                                  ellipsis
                                  style={{ fontSize: token.fontSizeSM }}
                                >
                                  {hasLiveMetrics && (metric?.cpuCores ?? 0) > 0
                                    ? t.hosts.metricCpuCores.replace('{n}', String(metric!.cpuCores))
                                    : t.hosts.metricCpu}
                                </Typography.Text>
                                <Typography.Text
                                  style={{
                                    fontSize: token.fontSizeSM,
                                    fontFamily: token.fontFamilyCode,
                                    fontWeight: hasLiveMetrics ? 500 : 400,
                                    color: hasLiveMetrics
                                      ? getMetricValueColor(cpuPct)
                                      : token.colorTextSecondary,
                                    flexShrink: 0,
                                  }}
                                >
                                  {hasLiveMetrics ? `${cpuPct.toFixed(1)}%` : '—'}
                                </Typography.Text>
                              </Flex>
                              <div
                                style={{
                                  width: '100%',
                                  height: 6,
                                  borderRadius: token.borderRadiusXS,
                                  background: token.colorFillSecondary,
                                  overflow: 'hidden',
                                }}
                              >
                                {hasLiveMetrics && (
                                  <div
                                    style={{
                                      width: `${cpuPct}%`,
                                      height: '100%',
                                      borderRadius: token.borderRadiusXS,
                                      background: getMetricColor(cpuPct),
                                      transition: 'width 0.25s ease, background-color 0.2s ease',
                                    }}
                                  />
                                )}
                              </div>
                            </div>
                          </Tooltip>
                        </Col>

                        <Col span={12}>
                          <Tooltip
                            title={
                              hasLiveMetrics
                                ? t.hosts.metricMemTip
                                    .replace('{used}', formatCap(metric!.memUsedBytes))
                                    .replace(
                                      '{free}',
                                      formatCap(
                                        Math.max(0, metric!.memTotalBytes - metric!.memUsedBytes),
                                      ),
                                    )
                                    .replace('{total}', formatCap(metric!.memTotalBytes))
                                    .replace('{pct}', String(memPct))
                                : undefined
                            }
                          >
                            <div>
                              <Flex justify="space-between" align="center" gap={6} style={{ marginBottom: 5 }}>
                                <Typography.Text
                                  type="secondary"
                                  ellipsis
                                  style={{ fontSize: token.fontSizeSM }}
                                >
                                  {t.hosts.metricMem}
                                </Typography.Text>
                                <Typography.Text
                                  style={{
                                    fontSize: token.fontSizeSM,
                                    fontFamily: token.fontFamilyCode,
                                    fontWeight: hasLiveMetrics ? 500 : 400,
                                    color: hasLiveMetrics
                                      ? getMetricValueColor(memPct)
                                      : token.colorTextSecondary,
                                    flexShrink: 0,
                                  }}
                                >
                                  {hasLiveMetrics
                                    ? formatMemCompact(metric!.memUsedBytes, metric!.memTotalBytes)
                                    : '—'}
                                </Typography.Text>
                              </Flex>
                              <div
                                style={{
                                  width: '100%',
                                  height: 6,
                                  borderRadius: token.borderRadiusXS,
                                  background: token.colorFillSecondary,
                                  overflow: 'hidden',
                                }}
                              >
                                {hasLiveMetrics && (
                                  <div
                                    style={{
                                      width: `${memPct}%`,
                                      height: '100%',
                                      borderRadius: token.borderRadiusXS,
                                      background: getMetricColor(memPct),
                                      transition: 'width 0.25s ease, background-color 0.2s ease',
                                    }}
                                  />
                                )}
                              </div>
                            </div>
                          </Tooltip>
                        </Col>
                      </Row>

                      <div>
                        <Flex justify="space-between" align="center" gap={8} style={{ marginBottom: 5 }}>
                          <Flex align="center" gap={6} style={{ minWidth: 0 }}>
                            <Typography.Text
                              type="secondary"
                              style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                            >
                              {t.hosts.metricStorage}
                            </Typography.Text>
                            {hasLiveMetrics && disks.length > 1 && (
                              <span
                                style={{
                                  padding: '0 6px',
                                  borderRadius: token.borderRadiusXS,
                                  background: token.colorFillSecondary,
                                  color: token.colorTextSecondary,
                                  fontFamily: token.fontFamilyCode,
                                  fontSize: 12,
                                  lineHeight: '18px',
                                  flexShrink: 0,
                                }}
                              >
                                {t.hosts.metricVolumesBadge.replace('{n}', String(disks.length))}
                              </span>
                            )}
                          </Flex>

                          <Typography.Text
                            style={{
                              fontSize: token.fontSizeSM,
                              fontFamily: token.fontFamilyCode,
                              fontWeight: hasLiveMetrics && disks.length > 0 ? 500 : 400,
                              color:
                                hasLiveMetrics && disks.length > 0
                                  ? token.colorText
                                  : token.colorTextSecondary,
                              flexShrink: 0,
                            }}
                          >
                            {hasLiveMetrics && disks.length > 0 && totalDiskBytes > 0 ? (
                              <>
                                {t.hosts.metricFreePrefix} {formatCap(totalFreeBytes)}{' '}
                                <span style={{ color: token.colorTextSecondary, fontWeight: 400 }}>
                                  / {formatCap(totalDiskBytes)}
                                </span>
                              </>
                            ) : (
                              '—'
                            )}
                          </Typography.Text>
                        </Flex>

                        {hasLiveMetrics && disks.length > 0 ? (
                          <Flex align="center" gap={4}>
                            {disks.map((d, idx) => {
                              const rawPct =
                                d.totalBytes > 0
                                  ? Math.round((d.usedBytes / d.totalBytes) * 100)
                                  : 0
                              const clampedPct = Math.max(0, Math.min(100, rawPct))
                              const barWidth = d.usedBytes > 0 ? Math.max(2, clampedPct) : 0
                              const tip = t.hosts.metricDiskTip
                                .replace('{name}', formatDiskTitle(d))
                                .replace('{free}', formatCap(d.freeBytes))
                                .replace('{total}', formatCap(d.totalBytes))
                                .replace('{used}', formatCap(d.usedBytes))
                                .replace('{pct}', String(clampedPct))

                              return (
                                <Tooltip key={d.mount || idx} title={tip} mouseEnterDelay={0.12}>
                                  <div
                                    title={tip}
                                    style={{
                                      flex: 1,
                                      minWidth: 6,
                                      height: 8,
                                      borderRadius: token.borderRadiusXS,
                                      background: token.colorFillSecondary,
                                      overflow: 'hidden',
                                    }}
                                  >
                                    <div
                                      style={{
                                        width: `${barWidth}%`,
                                        height: '100%',
                                        borderRadius: token.borderRadiusXS,
                                        background: getMetricColor(clampedPct),
                                        transition: 'width 0.25s ease, background-color 0.2s ease',
                                      }}
                                    />
                                  </div>
                                </Tooltip>
                              )
                            })}
                          </Flex>
                        ) : (
                          <div
                            style={{
                              width: '100%',
                              height: 8,
                              borderRadius: token.borderRadiusXS,
                              background: token.colorFillSecondary,
                              overflow: 'hidden',
                            }}
                          />
                        )}
                      </div>
                    </div>

                    {h.transport === 'ssh' && Boolean(h.sshHostKeyChangedAt) && (
                      <Alert
                        type="warning"
                        showIcon
                        icon={<WarningOutlined />}
                        title={
                          <span style={{ fontSize: token.fontSizeSM }}>
                            {t.hosts.fingerprintChanged}
                            {h.sshHostKeyChangedAt
                              ? ` · ${new Date(h.sshHostKeyChangedAt * 1000).toLocaleString(locale)}`
                              : ''}
                          </span>
                        }
                        description={
                          h.sshPrevHostKey ? (
                            <Typography.Text
                              type="secondary"
                              style={{ fontSize: token.fontSizeSM, fontFamily: token.fontFamilyCode }}
                            >
                              {t.hosts.fingerprintPrevious}: {h.sshPrevHostKey}
                            </Typography.Text>
                          ) : undefined
                        }
                      />
                    )}
                  </Space>
                </Card>
              </Col>
            )
          })}
        </Row>
      )}

      <Modal
        open={modalOpen}
        title={
          editing ? (
            <Flex align="center" justify="space-between" wrap gap={8} style={{ paddingRight: 28 }}>
              <span>{t.hosts.editHostTitle.replace('{name}', editing.name)}</span>
              {effectiveTransport !== 'local' && (
                <span
                  style={{
                    fontSize: token.fontSizeSM,
                    fontWeight: 500,
                    padding: '2px 10px',
                    borderRadius: 999,
                    color: canKeepCurrentCred ? token.colorSuccess : token.colorWarning,
                    background: canKeepCurrentCred ? token.colorSuccessBg : token.colorWarningBg,
                  }}
                >
                  {canKeepCurrentCred ? t.hosts.credSavedPill : t.hosts.credSwitchedPill}
                </span>
              )}
            </Flex>
          ) : (
            t.hosts.addHost
          )
        }
        width={620}
        onCancel={() => setModalOpen(false)}
        footer={
          <Flex justify="flex-end" gap={8}>
            <Button onClick={() => setModalOpen(false)}>{t.ignoreModal.cancel}</Button>
            <Button
              icon={<SyncOutlined spin={testing} />}
              loading={testing}
              onClick={() => runTest()}
            >
              {testing ? t.hosts.testing : t.hosts.testBtn}
            </Button>
            <Button type="primary" loading={saving} onClick={() => handleSave()}>
              {t.hosts.saveBtn}
            </Button>
          </Flex>
        }
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {/* 1. 平台选择横向卡片（表驱动） */}
          {visiblePlatforms.length > 1 && (
            <div>
              <Typography.Text strong style={{ display: 'block', marginBottom: 8 }}>
                {t.hosts.platformLabel}
              </Typography.Text>
              <Row gutter={[12, 12]}>
                {visiblePlatforms.map((p) => {
                  const meta = platformMeta(p.id)
                  const active = platformId === p.id
                  return (
                    <Col key={p.id} xs={24} sm={visiblePlatforms.length === 3 ? 8 : 12}>
                      <Card
                        size="small"
                        hoverable
                        onClick={() => selectPlatform(p.id)}
                        style={{
                          height: '100%',
                          borderColor: active ? token.colorPrimary : token.colorBorderSecondary,
                          background: active ? token.colorPrimaryBg : token.colorBgContainer,
                        }}
                      >
                        <Flex align="flex-start" gap={10}>
                          {meta.icon}
                          <div style={{ minWidth: 0 }}>
                            <Typography.Text strong style={{ display: 'block' }}>
                              {meta.title}
                            </Typography.Text>
                            <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                              {meta.desc}
                            </Typography.Text>
                          </div>
                        </Flex>
                      </Card>
                    </Col>
                  )
                })}
              </Row>
            </div>
          )}

          <Form form={form} layout="vertical" style={{ marginTop: 4 }}>
            <Form.Item name="kind" hidden>
              <Input />
            </Form.Item>
            <Form.Item name="transport" hidden>
              <Input />
            </Form.Item>

            {(platformId === 'fnos' || platformId === 'openwrt') && (
              <Alert
                type="info"
                showIcon
                style={{ marginBottom: 16 }}
                title={
                  <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                    {platformId === 'fnos' ? t.hosts.fnosSshGuide : t.hosts.openwrtSshGuide}
                  </Typography.Text>
                }
              />
            )}

            {/* 2. 连接方式切换（当平台支持多种连接方式时显示 Segmented + 对称优缺点提示卡片） */}
            {currentSpec.transports.length > 1 && (
              <Form.Item label={t.hosts.connMethodLabel} style={{ marginBottom: 16 }}>
                <div>
                  <Segmented
                    block
                    value={transportVal || currentSpec.defaultTransport}
                    onChange={(v) => {
                      setTestResult(null)
                      form.setFieldsValue({ transport: v as HostInput['transport'] })
                    }}
                    options={currentSpec.transports.map((tr) => ({
                      label: tr === 'dsm_api' ? t.hosts.connDsmApi : t.hosts.connSsh,
                      value: tr,
                    }))}
                  />
                  <Card
                    size="small"
                    style={{
                      marginTop: 10,
                      background: token.colorFillAlter,
                    }}
                  >
                    <Space direction="vertical" size={4} style={{ width: '100%' }}>
                      <div>
                        <Typography.Text
                          strong
                          style={{ color: token.colorSuccess, fontSize: token.fontSizeSM }}
                        >
                          {t.hosts.connProsLabel}
                        </Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                          {(transportVal || currentSpec.defaultTransport) === 'dsm_api'
                            ? t.hosts.connDsmPros
                            : t.hosts.connSshPros}
                        </Typography.Text>
                      </div>
                      <div>
                        <Typography.Text
                          strong
                          style={{ color: token.colorWarning, fontSize: token.fontSizeSM }}
                        >
                          {t.hosts.connLimitLabel}
                        </Typography.Text>
                        <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                          {(transportVal || currentSpec.defaultTransport) === 'dsm_api'
                            ? t.hosts.connDsmLimit
                            : t.hosts.connSshLimit}
                        </Typography.Text>
                      </div>
                    </Space>
                  </Card>
                </div>
              </Form.Item>
            )}

            {/* 3. 表单输入区（不放任何 2FA 字段，检测到 needOtp 时即时弹窗） */}
            <Form.Item name="name" label={t.hosts.nameLabel} rules={[{ required: true }]}>
              <Input placeholder={t.hosts.namePlaceholder} />
            </Form.Item>

            {transportVal === 'local' && (
              <Alert type="info" showIcon title={t.hosts.connLocal} />
            )}

            {transportVal === 'dsm_api' && (
              <>
                <Form.Item name="dsmUrl" label={t.hosts.dsmUrlLabel} rules={[{ required: true }]}>
                  <Input
                    placeholder="http://192.168.1.10:5000"
                    style={{ fontFamily: token.fontFamilyCode }}
                  />
                </Form.Item>
                <Row gutter={12}>
                  <Col xs={24} sm={12}>
                    <Form.Item name="dsmUser" label={t.hosts.dsmUserLabel} rules={[{ required: true }]}>
                      <Input placeholder="admin" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={12}>
                    <Form.Item
                      name="dsmSecret"
                      label={
                        canKeepDsmSecret ? (
                          <span>
                            {t.hosts.dsmSecretLabel}
                            <Typography.Text
                              type="secondary"
                              style={{ fontWeight: 400, marginLeft: 4 }}
                            >
                              {t.hosts.optionalKeepSuffix}
                            </Typography.Text>
                          </span>
                        ) : (
                          t.hosts.dsmSecretLabel
                        )
                      }
                      extra={canKeepDsmSecret ? t.hosts.keepPasswordHint : undefined}
                      rules={[
                        { required: !canKeepDsmSecret, message: t.hosts.passwordRequiredMsg },
                      ]}
                    >
                      <Input.Password
                        placeholder={
                          canKeepDsmSecret
                            ? t.hosts.keepPasswordPlaceholder
                            : t.hosts.enterPasswordPlaceholder
                        }
                      />
                    </Form.Item>
                  </Col>
                </Row>
              </>
            )}

            {transportVal === 'ssh' && (
              <>
                <Row gutter={12}>
                  <Col span={17}>
                    <Form.Item name="address" label={t.hosts.addressLabel} rules={[{ required: true }]}>
                      <Input
                        placeholder="192.168.1.10"
                        style={{ fontFamily: token.fontFamilyCode }}
                      />
                    </Form.Item>
                  </Col>
                  <Col span={7}>
                    <Form.Item name="port" label={t.hosts.portLabel}>
                      <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                </Row>
                <Row gutter={12}>
                  <Col xs={24} sm={12}>
                    <Form.Item name="sshUser" label={t.hosts.userLabel} rules={[{ required: true }]}>
                      <Input placeholder="root / admin" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={12}>
                    <Form.Item name="sshAuth" label={t.hosts.authTypeLabel}>
                      <Radio.Group
                        onChange={() => form.setFields([{ name: 'sshSecret', errors: [] }])}
                      >
                        <Radio value="password">{t.hosts.authPassword}</Radio>
                        <Radio value="key">{t.hosts.authKey}</Radio>
                      </Radio.Group>
                    </Form.Item>
                  </Col>
                </Row>
                {authVal === 'key' ? (
                  <>
                    <Space style={{ marginBottom: 8 }}>
                      <Button size="small" icon={<KeyOutlined />} onClick={handleGenerateKey}>
                        {t.hosts.generateKey}
                      </Button>
                    </Space>
                    {generatedPubKey && (
                      <Alert
                        type="info"
                        style={{ marginBottom: 12 }}
                        title={t.hosts.generatedPubTip}
                        description={
                          <Space direction="vertical" style={{ width: '100%' }}>
                            <Typography.Text
                              code
                              style={{ fontSize: token.fontSizeSM, wordBreak: 'break-all' }}
                            >
                              {generatedPubKey}
                            </Typography.Text>
                            <Button
                              size="small"
                              icon={<CopyOutlined />}
                              onClick={() => {
                                navigator.clipboard.writeText(generatedPubKey)
                                message.success(t.detail.copied)
                              }}
                            >
                              {t.detail.copyCmd}
                            </Button>
                          </Space>
                        }
                      />
                    )}
                    <Form.Item
                      name="sshSecret"
                      label={
                        canKeepSshKey ? (
                          <span>
                            {t.hosts.privateKeyLabel}
                            <Typography.Text
                              type="secondary"
                              style={{ fontWeight: 400, marginLeft: 4 }}
                            >
                              {t.hosts.optionalKeepSuffix}
                            </Typography.Text>
                          </span>
                        ) : (
                          t.hosts.privateKeyLabel
                        )
                      }
                      extra={canKeepSshKey ? t.hosts.keepPrivateKeyHint : undefined}
                      rules={[
                        { required: !canKeepSshKey, message: t.hosts.privateKeyRequiredMsg },
                      ]}
                    >
                      <Input.TextArea
                        rows={3}
                        placeholder={
                          canKeepSshKey
                            ? t.hosts.keepPrivateKeyPlaceholder
                            : '-----BEGIN OPENSSH PRIVATE KEY-----'
                        }
                        style={{ fontFamily: token.fontFamilyCode }}
                      />
                    </Form.Item>
                    <Form.Item
                      name="sshPassphrase"
                      label={t.hosts.passphraseLabel}
                      style={{ marginBottom: 4 }}
                    >
                      <Input.Password
                        placeholder={canKeepSshKey ? t.hosts.keepPassphrasePlaceholder : undefined}
                      />
                    </Form.Item>
                  </>
                ) : (
                  <Form.Item
                    name="sshSecret"
                    label={
                      canKeepSshPassword ? (
                        <span>
                          {t.hosts.passwordLabel}
                          <Typography.Text
                            type="secondary"
                            style={{ fontWeight: 400, marginLeft: 4 }}
                          >
                            {t.hosts.optionalKeepSuffix}
                          </Typography.Text>
                        </span>
                      ) : (
                        t.hosts.passwordLabel
                      )
                    }
                    extra={canKeepSshPassword ? t.hosts.keepPasswordHint : undefined}
                    rules={[
                      { required: !canKeepSshPassword, message: t.hosts.passwordRequiredMsg },
                    ]}
                    style={{ marginBottom: 4 }}
                  >
                    <Input.Password
                      placeholder={
                        canKeepSshPassword
                          ? t.hosts.keepPasswordPlaceholder
                          : t.hosts.enterPasswordPlaceholder
                      }
                    />
                  </Form.Item>
                )}
              </>
            )}
          </Form>

          {/* 4. 内联测试诊断结果 */}
          {testResult && (
            <Card
              size="small"
              style={{
                background: token.colorFillAlter,
              }}
            >
              <Space direction="vertical" size="small" style={{ width: '100%' }}>
                <Alert
                  type={testResult.ok ? 'success' : 'error'}
                  showIcon
                  title={testResult.ok ? t.hosts.testPassed : t.hosts.testFailed}
                />
                <Timeline
                  style={{ marginTop: 8, marginBottom: 0 }}
                  items={testResult.steps.map((s) => ({
                    color: s.status === 'ok' ? 'green' : 'red',
                    dot: s.status === 'ok' ? <CheckCircleOutlined /> : <CloseCircleOutlined />,
                    children: (
                      <div>
                        <Typography.Text strong>{s.step}</Typography.Text>
                        <div>
                          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                            {s.message}
                          </Typography.Text>
                        </div>
                      </div>
                    ),
                  }))}
                />
                {transportVal === 'ssh' && testResult.hostKeyChanged && (
                  <Alert
                    type="warning"
                    showIcon
                    icon={<WarningOutlined />}
                    title={t.hosts.fingerprintChanged}
                    description={
                      testResult.previousHostKey
                        ? `${t.hosts.fingerprintPrevious}: ${testResult.previousHostKey}`
                        : undefined
                    }
                  />
                )}
              </Space>
            </Card>
          )}
        </Space>
      </Modal>

      {/* 2FA 即时弹窗（仅当测试或保存返回 needOtp 时弹出） */}
      <Modal
        open={otpModalOpen}
        title={t.hosts.otpModalTitle}
        width={380}
        onCancel={() => setOtpModalOpen(false)}
        footer={
          <Flex justify="flex-end" gap={8}>
            <Button onClick={() => setOtpModalOpen(false)}>{t.ignoreModal.cancel}</Button>
            <Button
              type="primary"
              loading={testing || saving}
              onClick={onConfirmOtpModal}
            >
              {otpTrigger === 'test' ? t.hosts.otpVerifyTestBtn : t.hosts.otpVerifySaveBtn}
            </Button>
          </Flex>
        }
      >
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {t.hosts.otpModalDesc}
          </Typography.Text>
          <Input
            autoFocus
            size="large"
            maxLength={6}
            placeholder="000000"
            value={otpCode}
            onChange={(e) => setOtpCode(e.target.value)}
            onPressEnter={onConfirmOtpModal}
            style={{
              textAlign: 'center',
              letterSpacing: '0.4em',
              fontFamily: token.fontFamilyCode,
            }}
          />
        </Space>
      </Modal>
    </Space>
  )
}
