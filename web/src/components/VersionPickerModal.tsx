import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Empty,
  Flex,
  Grid,
  Input,
  Modal,
  Radio,
  Row,
  Segmented,
  Select,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import type { TextAreaRef } from 'antd/es/input/TextArea'
import {
  ArrowRightOutlined,
  CheckOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons'
import {
  api,
  type AppCardItem,
  type ServiceEnvItem,
  type ServiceItem,
  type ServicePortItem,
  type ServiceTagItem,
  type ServiceVolumeItem,
  type UpdatePolicyMode,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import { detectComposeOverwriteRisks, replaceComposeImageTag } from './composeGuard'
import LockableVersionBar from './LockableVersionBar'
import { compareNumericSegs, parseNumericSegs } from './versionUtils'

export interface VersionPickSelection {
  targetTag: string
  policyMode: UpdatePolicyMode
  policyPrefix?: string
  skipTargetTag?: string
}

interface VersionPickerModalProps {
  open: boolean
  appName: string
  subtitle: string
  service: ServiceItem | null
  app?: AppCardItem
  initialTab?: 'version' | 'config'
  initialSelectedTag?: string
  onPickOnly?: (sel: VersionPickSelection) => void
  onClose: () => void
  onUpdated?: () => void
}

interface EditableEnvRow extends ServiceEnvItem {
  _id: number
}

interface EditablePortRow extends ServicePortItem {
  _id: number
}

function extractShortRepo(imageRef: string): string {
  const withoutDigest = imageRef.split('@')[0]
  const lastSlash = withoutDigest.lastIndexOf('/')
  const colonAfterSlash = withoutDigest.indexOf(':', lastSlash + 1)
  const repoPath = colonAfterSlash >= 0 ? withoutDigest.slice(0, colonAfterSlash) : withoutDigest
  return repoPath
    .replace(/^docker\.io\/library\//, '')
    .replace(/^docker\.io\//, '')
    .replace(/^index\.docker\.io\/library\//, '')
    .replace(/^index\.docker\.io\//, '')
    .replace(/^library\//, '')
}

function formatTagTime(ts?: number): string {
  if (!ts || ts <= 0) return ''
  const d = new Date(ts * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function cleanInitialTag(svc: ServiceItem, explicit?: string): string {
  if (explicit && explicit.trim()) return explicit.trim()
  if (svc.rawTargetVersion && !svc.rawTargetVersion.includes(' ')) return svc.rawTargetVersion
  if (svc.targetVersion && !svc.targetVersion.includes(' ')) return svc.targetVersion
  if (svc.currentVersion && !svc.currentVersion.includes(' ')) return svc.currentVersion
  const colonIdx = svc.imageRef.lastIndexOf(':')
  const slashIdx = svc.imageRef.lastIndexOf('/')
  if (colonIdx > slashIdx) {
    return svc.imageRef.slice(colonIdx + 1).split('@')[0]
  }
  return 'latest'
}

function isValidPortStr(val: string): boolean {
  const trimmed = val.trim()
  if (!/^\d+$/.test(trimmed)) return false
  const n = Number(trimmed)
  return n >= 1 && n <= 65535
}

export default function VersionPickerModal(props: VersionPickerModalProps) {
  const screens = Grid.useBreakpoint()
  return (
    <Modal
      open={props.open}
      onCancel={props.onClose}
      footer={null}
      width={screens.md ? 760 : '95vw'}
      destroyOnHidden
      styles={{ body: { paddingTop: 4 } }}
      title={null}
      closable={false}
    >
      {props.open && props.service && (
        <VersionPickerModalBody
          key={`${props.service.key}::${props.initialSelectedTag ?? ''}::${props.initialTab ?? 'version'}`}
          {...props}
          service={props.service}
        />
      )}
    </Modal>
  )
}

function VersionPickerModalBody({
  appName,
  subtitle,
  service,
  app,
  initialTab = 'version',
  initialSelectedTag,
  onPickOnly,
  onClose,
  onUpdated,
}: VersionPickerModalProps & { service: ServiceItem }) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const screens = Grid.useBreakpoint()
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)
  const textareaRef = useRef<TextAreaRef | null>(null)
  const nextRowIdRef = useRef<number>(1)

  const canShowConfigTab = Boolean(!onPickOnly && app && app.source !== 'vendor_managed')
  const [tab, setTab] = useState<'version' | 'config'>(
    canShowConfigTab ? initialTab : 'version',
  )

  const [localTags, setLocalTags] = useState<ServiceTagItem[]>([])
  const [remoteTags, setRemoteTags] = useState<ServiceTagItem[]>([])
  const [highestApiTag, setHighestApiTag] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [searchingRemote, setSearchingRemote] = useState(false)
  const [remoteSearched, setRemoteSearched] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedTag, setSelectedTag] = useState(() =>
    cleanInitialTag(service, initialSelectedTag),
  )
  const [policy, setPolicy] = useState<UpdatePolicyMode>('all')
  const [lockDepth, setLockDepth] = useState(1)
  const [submitting, setSubmitting] = useState(false)

  // 按新版配置更新 —— 分支 A：单容器 YAML 项目编辑状态
  const isStandaloneConfigMode = Boolean(
    app && !app.composeEditable && app.source === 'standalone',
  )
  const [yamlLoaded, setYamlLoaded] = useState(false)
  const [yamlEditable, setYamlEditable] = useState(true)
  const [yamlReason, setYamlReason] = useState('')
  const [origYaml, setOrigYaml] = useState('')
  const [yamlText, setYamlText] = useState('')
  const lastSyncedYamlTagRef = useRef<string>('')

  // 按新版配置更新 —— 分支 B：无 YAML 纯独立容器（环境变量 + 端口映射）编辑状态
  const [standaloneLoaded, setStandaloneLoaded] = useState(false)
  const [standaloneEditable, setStandaloneEditable] = useState(true)
  const [standaloneReason, setStandaloneReason] = useState('')
  const [envRows, setEnvRows] = useState<EditableEnvRow[]>([])
  const [portRows, setPortRows] = useState<EditablePortRow[]>([])
  const [origVolumes, setOrigVolumes] = useState<ServiceVolumeItem[] | undefined>(undefined)

  const [submittingConfig, setSubmittingConfig] = useState(false)

  // 计算当前已知最高版本标签
  const highestTag = useMemo(() => {
    if (highestApiTag) return highestApiTag
    const pool = remoteTags.length > 0 ? remoteTags : localTags
    const firstVer = pool.find((item) => item.tag !== 'latest')
    if (firstVer) return firstVer.tag
    if (service.rawTargetVersion && !service.rawTargetVersion.includes(' ')) {
      return service.rawTargetVersion
    }
    if (service.targetVersion && !service.targetVersion.includes(' ')) {
      return service.targetVersion
    }
    return pool[0]?.tag || ''
  }, [highestApiTag, localTags, remoteTags, service])

  const applyTagSelection = (tag: string, knownHighest: string) => {
    setSelectedTag(tag)
    setLockDepth(1)
    const isLatest = !knownHighest || tag === knownHighest || tag === 'latest'
    setPolicy(isLatest ? 'all' : 'skip_latest')
  }

  useEffect(() => {
    let cancelled = false
    const initTag = cleanInitialTag(service, initialSelectedTag)

    ;(async () => {
      try {
        let res = await api.searchServiceTags(service.key, '', false)
        if (!cancelled && (!res.items || res.items.length === 0)) {
          res = await api.searchServiceTags(service.key, '', true)
        }
        if (cancelled) return
        const items = res.items ?? []
        setLocalTags(items)
        const hi =
          res.highestTag ||
          items.find((i) => i.tag !== 'latest')?.tag ||
          (service.rawTargetVersion && !service.rawTargetVersion.includes(' ')
            ? service.rawTargetVersion
            : '')
        setHighestApiTag(hi)
        const pick =
          initialSelectedTag ||
          (service.targetVersion && !service.targetVersion.includes(' ')
            ? service.targetVersion
            : '') ||
          items[0]?.tag ||
          initTag
        const isLatest = !hi || pick === hi || pick === 'latest'
        setSelectedTag(pick)
        setPolicy(isLatest ? 'all' : 'skip_latest')
      } catch {
        if (!cancelled) {
          setLocalTags([])
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
  }, [service, initialSelectedTag])

  // 加载单容器 YAML 配置（Case A）
  useEffect(() => {
    if (tab !== 'config' || !app || isStandaloneConfigMode || yamlLoaded) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.getAppCompose(app.key)
        if (cancelled) return
        setYamlEditable(res.editable)
        setYamlReason(res.reason || '')
        const raw = res.content || ''
        setOrigYaml(raw)
        const synced = replaceComposeImageTag(raw, service.imageRef, selectedTag)
        setYamlText(synced)
        lastSyncedYamlTagRef.current = selectedTag
        setYamlLoaded(true)
      } catch (e) {
        if (!cancelled) {
          setYamlEditable(false)
          setYamlReason((e as Error).message)
          setYamlLoaded(true)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [tab, app, isStandaloneConfigMode, yamlLoaded, service.imageRef, selectedTag])

  // 当用户在左页签切换了 selectedTag 时，自动同步更新 YAML 预览中的 image: 标签
  useEffect(() => {
    if (!yamlLoaded || !yamlEditable || !selectedTag) return
    if (lastSyncedYamlTagRef.current === selectedTag) return
    lastSyncedYamlTagRef.current = selectedTag
    setYamlText((prev) => replaceComposeImageTag(prev, service.imageRef, selectedTag))
  }, [yamlLoaded, yamlEditable, selectedTag, service.imageRef])

  // 加载纯独立容器配置（Case B）
  useEffect(() => {
    if (tab !== 'config' || !isStandaloneConfigMode || standaloneLoaded) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.getServiceConfig(service.key)
        if (cancelled) return
        setStandaloneEditable(res.editable)
        setStandaloneReason(res.reason || '')
        setEnvRows(
          (res.env ?? []).map((item) => ({
            _id: nextRowIdRef.current++,
            key: item.key,
            value: item.value,
          })),
        )
        setPortRows(
          (res.ports ?? []).map((item) => ({
            _id: nextRowIdRef.current++,
            hostIp: item.hostIp,
            hostPort: item.hostPort,
            containerPort: item.containerPort,
            protocol: (item.protocol || 'tcp').toLowerCase(),
          })),
        )
        setOrigVolumes(res.volumes)
        setStandaloneLoaded(true)
      } catch (e) {
        if (!cancelled) {
          setStandaloneEditable(false)
          setStandaloneReason((e as Error).message)
          setStandaloneLoaded(true)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [tab, isStandaloneConfigMode, standaloneLoaded, service.key])

  const handleRemoteSearch = async () => {
    if (!service) return
    const q = query.trim()
    setSearchingRemote(true)
    try {
      const res = await api.searchServiceTags(service.key, q, true)
      setRemoteTags(res.items ?? [])
      if (res.highestTag) {
        setHighestApiTag(res.highestTag)
      }
      setRemoteSearched(true)
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSearchingRemote(false)
    }
  }

  const trimmedQuery = query.trim()
  const activePool = remoteSearched ? remoteTags : localTags
  const displayedTags = useMemo(() => {
    const base = trimmedQuery
      ? activePool.filter((item) => item.tag.toLowerCase().includes(trimmedQuery.toLowerCase()))
      : localTags
    return base.slice(0, 10)
  }, [activePool, localTags, trimmedQuery])

  const allCombinedTags = useMemo(() => {
    const seen = new Set<string>()
    const merged: ServiceTagItem[] = []
    for (const it of [...localTags, ...remoteTags]) {
      if (!seen.has(it.tag)) {
        seen.add(it.tag)
        merged.push(it)
      }
    }
    return merged
  }, [localTags, remoteTags])

  const isLatest = !highestTag || selectedTag === highestTag || selectedTag === 'latest'
  const numericSegs = useMemo(() => parseNumericSegs(selectedTag), [selectedTag])
  const safeLockDepth = Math.min(Math.max(1, lockDepth), Math.max(1, numericSegs.length - 1))
  const lockedPrefix = numericSegs.length >= 2 ? numericSegs.slice(0, safeLockDepth).join('.') : ''

  // 实时复弹预警检测
  const conflictVer = useMemo(() => {
    if (!selectedTag) return ''
    if (policy === 'all' && !isLatest && highestTag) {
      return highestTag
    }
    if (policy === 'prefix' && lockedPrefix) {
      const selectedIdx = allCombinedTags.findIndex((r) => r.tag === selectedTag)
      const hit = allCombinedTags.find((r, idx) => {
        if (r.tag === selectedTag) return false
        const clean = r.tag.replace(/^v/i, '')
        const matchesPrefix = clean === lockedPrefix || clean.startsWith(`${lockedPrefix}.`)
        if (!matchesPrefix) return false
        const cmp = compareNumericSegs(r.tag, selectedTag)
        if (cmp !== 0) return cmp > 0
        return selectedIdx >= 0 && idx < selectedIdx
      })
      if (hit) return hit.tag
    }
    return ''
  }, [allCombinedTags, highestTag, isLatest, lockedPrefix, policy, selectedTag])

  const syncedOrigYaml = useMemo(
    () => replaceComposeImageTag(origYaml, service.imageRef, selectedTag),
    [origYaml, service.imageRef, selectedTag],
  )

  const overwriteRisks = useMemo(
    () => detectComposeOverwriteRisks(origYaml, yamlText),
    [origYaml, yamlText],
  )

  const handleSubmitVersion = async () => {
    if (!service || !selectedTag) return
    const selection: VersionPickSelection = {
      targetTag: selectedTag,
      policyMode: policy,
      policyPrefix: policy === 'prefix' ? lockedPrefix : undefined,
      skipTargetTag: policy === 'skip_latest' ? highestTag : undefined,
    }

    if (onPickOnly) {
      onPickOnly(selection)
      onClose()
      return
    }

    setSubmitting(true)
    try {
      const res = await api.startUpdates({
        items: [
          {
            serviceKey: service.key,
            targetTag: selection.targetTag,
            policyMode: selection.policyMode,
            policyPrefix: selection.policyPrefix,
            skipTargetTag: selection.skipTargetTag,
          },
        ],
      })
      if (res.items) {
        for (const job of res.items) {
          upsertUpdateJob(job)
        }
      }
      message.info(t.home.updating)
      onUpdated?.()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const handleClearAndPaste = async () => {
    try {
      if (navigator.clipboard?.readText) {
        const clip = await navigator.clipboard.readText()
        if (clip && clip.trim()) {
          setYamlText(replaceComposeImageTag(clip, service.imageRef, selectedTag))
          textareaRef.current?.focus()
          return
        }
      }
    } catch {
      // 剪贴板未授权时降级为清空并聚焦输入框
    }
    setYamlText('')
    textareaRef.current?.focus()
    message.info(t.projectUpdate.clearedPasteTip)
  }

  const handleSubmitConfig = async () => {
    if (!app) return
    const policyPrefixVal = policy === 'prefix' ? lockedPrefix : undefined
    const skipTargetVal = policy === 'skip_latest' ? highestTag : undefined

    if (!isStandaloneConfigMode) {
      if (!yamlText.trim()) {
        message.warning(t.projectUpdate.yamlEmptyError)
        return
      }
      setSubmittingConfig(true)
      try {
        const res = await api.updateAppCompose({
          appKey: app.key,
          content: yamlText,
          serviceKey: service.key,
          policyMode: policy,
          policyPrefix: policyPrefixVal,
          skipTargetTag: skipTargetVal,
        })
        if (res.job) {
          upsertUpdateJob(res.job)
        }
        if (res.items) {
          for (const job of res.items) {
            upsertUpdateJob(job)
          }
        }
        message.success(t.versionPicker.configUpdateStarted)
        onUpdated?.()
        onClose()
      } catch (e) {
        message.error((e as Error).message)
      } finally {
        setSubmittingConfig(false)
      }
      return
    }

    // 分支 B：无 YAML 纯独立容器配置提交
    const cleanedEnv: ServiceEnvItem[] = []
    for (const row of envRows) {
      const k = row.key.trim()
      const v = row.value
      if (!k && !v.trim()) continue
      if (!k) {
        message.warning(t.versionPicker.envKeyRequired)
        return
      }
      cleanedEnv.push({ key: k, value: v })
    }

    const cleanedPorts: ServicePortItem[] = []
    for (const row of portRows) {
      const hp = row.hostPort.trim()
      const cp = row.containerPort.trim()
      if (!hp && !cp) continue
      if (!isValidPortStr(hp) || !isValidPortStr(cp)) {
        message.warning(t.versionPicker.portInvalidMsg)
        return
      }
      cleanedPorts.push({
        ...(row.hostIp ? { hostIp: row.hostIp } : {}),
        hostPort: hp,
        containerPort: cp,
        protocol: (row.protocol || 'tcp').toLowerCase(),
      })
    }

    setSubmittingConfig(true)
    try {
      const res = await api.updateServiceConfig({
        serviceKey: service.key,
        targetTag: selectedTag,
        policyMode: policy,
        policyPrefix: policyPrefixVal,
        skipTargetTag: skipTargetVal,
        env: cleanedEnv,
        ports: cleanedPorts,
        volumes: origVolumes,
      })
      if (res.job) {
        upsertUpdateJob(res.job)
      }
      message.success(t.versionPicker.configUpdateStarted)
      onUpdated?.()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmittingConfig(false)
    }
  }

  const shortRepo = extractShortRepo(service.imageRef)
  const curVer = service.currentVersion || t.home.unknownCurrent
  const exactTagExists = allCombinedTags.some((r) => r.tag === trimmedQuery)

  const renderRadioCard = (
    key: UpdatePolicyMode,
    titleText: string,
    descNode: React.ReactNode,
    extraNode?: React.ReactNode,
  ) => {
    const checked = policy === key
    return (
      <div
        key={key}
        onClick={() => setPolicy(key)}
        style={{
          padding: '10px 12px',
          borderRadius: token.borderRadiusLG,
          border: `1px solid ${checked ? token.colorPrimary : token.colorBorderSecondary}`,
          background: checked ? token.colorPrimaryBg : token.colorBgContainer,
          cursor: 'pointer',
          transition: 'border-color 0.15s ease, background-color 0.15s ease',
        }}
      >
        <Flex align="flex-start" gap={10}>
          <Radio
            checked={checked}
            onChange={() => setPolicy(key)}
            style={{ marginTop: 2, marginInlineEnd: 0 }}
          />
          <div style={{ flex: 1, minWidth: 0 }}>
            <Typography.Text
              strong
              style={{
                color: checked ? token.colorPrimary : token.colorText,
                display: 'block',
              }}
            >
              {titleText}
            </Typography.Text>
            {extraNode}
            {descNode && (
              <Typography.Text
                type="secondary"
                style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
              >
                {descNode}
              </Typography.Text>
            )}
          </div>
        </Flex>
      </div>
    )
  }

  const loadingConfig =
    tab === 'config' && (isStandaloneConfigMode ? !standaloneLoaded : !yamlLoaded)
  const configEditable = isStandaloneConfigMode ? standaloneEditable : yamlEditable
  const configReason = isStandaloneConfigMode ? standaloneReason : yamlReason

  return (
    <Flex vertical gap={16}>
      {/* 顶栏 */}
      <Flex
        wrap="wrap"
        align="center"
        justify="space-between"
        gap={12}
        style={{
          paddingBottom: 14,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <div style={{ minWidth: 0 }}>
          <Typography.Title level={5} style={{ margin: 0 }}>
            {(tab === 'config' ? t.versionPicker.configTitle : t.versionPicker.title).replace(
              '{name}',
              appName,
            )}
          </Typography.Title>
          <Typography.Text
            type="secondary"
            style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
          >
            {subtitle} · {t.versionPicker.runningNow}
            <Typography.Text code>{curVer}</Typography.Text>
          </Typography.Text>
        </div>

        <Flex wrap="wrap" align="center" gap={8} style={{ maxWidth: '100%' }}>
          {/* 目标镜像胶囊（在配置页签上支持一键点回选版页签重选版本） */}
          <Tag
            variant="filled"
            color="processing"
            style={{
              margin: 0,
              padding: '4px 10px',
              display: 'inline-flex',
              alignItems: 'center',
              gap: 6,
              maxWidth: '100%',
            }}
          >
            <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}>
              {t.versionPicker.targetImage}
            </Typography.Text>
            <Typography.Text
              strong
              ellipsis
              style={{
                fontFamily: token.fontFamilyCode,
                fontSize: token.fontSizeSM,
                color: token.colorPrimary,
              }}
            >
              {shortRepo}:{selectedTag}
            </Typography.Text>
            {tab === 'config' && (
              <Typography.Link
                onClick={() => setTab('version')}
                style={{ fontSize: token.fontSizeSM, marginLeft: 2, flexShrink: 0 }}
              >
                {t.versionPicker.changeVerLink}
              </Typography.Link>
            )}
          </Tag>

          {/* 右上角双页签切换：选择版本更新 | 按新版配置更新 */}
          {canShowConfigTab && (
            <Segmented
              value={tab}
              onChange={(val) => setTab(val as 'version' | 'config')}
              options={[
                { label: t.versionPicker.tabVersion, value: 'version' },
                { label: t.versionPicker.tabConfig, value: 'config' },
              ]}
            />
          )}
        </Flex>
      </Flex>

      {tab === 'version' ? (
        <>
          {/* 双栏主体：桌面左右 10:14，移动端上下叠放（100% 完整保留） */}
          <Row gutter={[20, 16]} align="stretch">
            <Col xs={24} md={10} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {/* 组合式搜索框（输入仅过滤本地列表，回车/点搜索请求远端） */}
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  allowClear
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value)
                    setRemoteSearched(false)
                  }}
                  onPressEnter={(e) => {
                    e.preventDefault()
                    handleRemoteSearch()
                  }}
                  placeholder={t.versionPicker.searchPlaceholder}
                  style={{ fontFamily: token.fontFamilyCode }}
                />
                <Button
                  type="primary"
                  ghost
                  loading={searchingRemote}
                  onClick={handleRemoteSearch}
                >
                  {t.versionPicker.searchBtn}
                </Button>
              </Space.Compact>

              <Typography.Text
                type="secondary"
                style={{ fontSize: token.fontSizeSM, paddingInline: 2 }}
              >
                {trimmedQuery
                  ? t.versionPicker.matchedCaption.replace('{q}', trimmedQuery)
                  : t.versionPicker.recentCaption}
              </Typography.Text>

              {/* 标签列表 */}
              <div
                role="listbox"
                style={{
                  flex: 1,
                  maxHeight: 260,
                  minHeight: 180,
                  overflowY: 'auto',
                  borderRadius: token.borderRadiusLG,
                  border: `1px solid ${token.colorBorderSecondary}`,
                  background: token.colorBgContainer,
                }}
              >
                {loading || searchingRemote ? (
                  <Flex align="center" justify="center" gap={8} style={{ height: 180 }}>
                    <Spin size="small" />
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {t.versionPicker.loadingTags}
                    </Typography.Text>
                  </Flex>
                ) : (
                  <>
                    {displayedTags.map((item, idx) => {
                      const active = item.tag === selectedTag
                      const timeStr = formatTagTime(item.updatedAt)
                      return (
                        <Flex
                          key={item.tag}
                          role="option"
                          aria-selected={active}
                          tabIndex={0}
                          align="center"
                          justify="space-between"
                          gap={8}
                          onClick={() => applyTagSelection(item.tag, highestTag)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault()
                              applyTagSelection(item.tag, highestTag)
                            }
                          }}
                          style={{
                            padding: '8px 12px',
                            borderBottom:
                              idx < displayedTags.length - 1
                                ? `1px solid ${token.colorSplit}`
                                : 'none',
                            background: active ? token.colorPrimaryBg : 'transparent',
                            cursor: 'pointer',
                            transition: 'background-color 0.15s ease',
                          }}
                        >
                          <Flex align="center" gap={8} style={{ minWidth: 0, flex: 1 }}>
                            <CheckOutlined
                              style={{
                                fontSize: 12,
                                color: token.colorPrimary,
                                visibility: active ? 'visible' : 'hidden',
                                flexShrink: 0,
                              }}
                            />
                            <Typography.Text
                              strong={active}
                              ellipsis
                              style={{
                                fontFamily: token.fontFamilyCode,
                                fontSize: token.fontSizeSM,
                                color: active ? token.colorPrimary : token.colorText,
                              }}
                            >
                              {item.tag}
                            </Typography.Text>
                          </Flex>
                          {timeStr && (
                            <Typography.Text
                              type="secondary"
                              style={{ fontSize: 11, flexShrink: 0 }}
                            >
                              {timeStr}
                            </Typography.Text>
                          )}
                        </Flex>
                      )
                    })}

                    {trimmedQuery && displayedTags.length === 0 && !remoteSearched && (
                      <div style={{ padding: 8 }}>
                        <Button type="link" block onClick={handleRemoteSearch}>
                          {t.versionPicker.notFoundInRecent.replace('{q}', trimmedQuery)}
                        </Button>
                      </div>
                    )}

                    {trimmedQuery &&
                      !exactTagExists &&
                      (remoteSearched || localTags.length === 0) && (
                        <Flex
                          role="option"
                          aria-selected={selectedTag === trimmedQuery}
                          tabIndex={0}
                          align="center"
                          justify="space-between"
                          gap={8}
                          onClick={() => applyTagSelection(trimmedQuery, highestTag)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault()
                              applyTagSelection(trimmedQuery, highestTag)
                            }
                          }}
                          style={{
                            padding: '9px 12px',
                            borderTop:
                              displayedTags.length > 0 ? `1px solid ${token.colorSplit}` : 'none',
                            background:
                              selectedTag === trimmedQuery ? token.colorPrimaryBg : 'transparent',
                            cursor: 'pointer',
                          }}
                        >
                          <Space size={6}>
                            <PlusOutlined style={{ fontSize: 11, color: token.colorPrimary }} />
                            <Typography.Text
                              strong
                              style={{
                                fontFamily: token.fontFamilyCode,
                                fontSize: token.fontSizeSM,
                              }}
                            >
                              {trimmedQuery}
                            </Typography.Text>
                          </Space>
                          <Typography.Text
                            style={{ fontSize: token.fontSizeSM, color: token.colorPrimary }}
                          >
                            {t.versionPicker.useCustomTag}
                          </Typography.Text>
                        </Flex>
                      )}

                    {!trimmedQuery && displayedTags.length === 0 && (
                      <Empty
                        image={Empty.PRESENTED_IMAGE_SIMPLE}
                        description={t.versionPicker.emptyTags}
                        style={{ marginBlock: 20 }}
                      />
                    )}
                  </>
                )}
              </div>
            </Col>

            {/* 右栏：更新完成后的提醒设置 */}
            <Col
              xs={24}
              md={14}
              style={{
                borderLeft: screens.md ? `1px solid ${token.colorSplit}` : 'none',
                paddingLeft: screens.md ? 20 : 10,
                display: 'flex',
                flexDirection: 'column',
                justifyContent: 'space-between',
                gap: 10,
              }}
            >
              <Flex vertical gap={8}>
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  {t.versionPicker.policyHeader}
                </Typography.Text>

                {/* 1. 正常提醒所有新版本 */}
                {renderRadioCard(
                  'all',
                  t.versionPicker.policyAllTitle,
                  t.versionPicker.policyAllDesc,
                )}

                {/* 2. 仅跳过当前最新版（选非最高版本时默认选中） */}
                {!isLatest &&
                  highestTag &&
                  renderRadioCard(
                    'skip_latest',
                    t.versionPicker.policySkipLatestTitle.replace('{ver}', highestTag),
                    t.versionPicker.policySkipLatestDesc.replace('{ver}', highestTag),
                  )}

                {/* 3. 锁定前面的版本号（至少 2 段数字版本号时展示） */}
                {numericSegs.length >= 2 &&
                  renderRadioCard(
                    'prefix',
                    t.versionPicker.policyPrefixTitle,
                    null,
                    <LockableVersionBar
                      segments={numericSegs}
                      depth={safeLockDepth}
                      active={policy === 'prefix'}
                      onDepthChange={(d) => {
                        setPolicy('prefix')
                        setLockDepth(d)
                      }}
                    />,
                  )}

                {/* 4. 不再提醒更新 */}
                {renderRadioCard(
                  'forever',
                  t.versionPicker.policyForeverTitle,
                  t.versionPicker.policyForeverDesc,
                )}

                {/* 复弹实时橙色预警 */}
                {conflictVer && (
                  <Alert
                    type="warning"
                    showIcon
                    title={
                      <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                        {t.versionPicker.immediateWarnBefore}
                        <Typography.Text code>{conflictVer}</Typography.Text>
                        {t.versionPicker.immediateWarnAfter}
                      </Typography.Text>
                    }
                  />
                )}
              </Flex>
            </Col>
          </Row>

          {/* 底栏 */}
          <Flex
            justify="flex-end"
            gap={8}
            style={{
              paddingTop: 14,
              borderTop: `1px solid ${token.colorSplit}`,
            }}
          >
            <Button onClick={onClose}>{t.versionPicker.cancel}</Button>
            <Button
              type="primary"
              loading={submitting}
              disabled={!selectedTag}
              onClick={handleSubmitVersion}
            >
              {onPickOnly ? t.versionPicker.confirmPickOnly : t.versionPicker.confirmUpdate}
            </Button>
          </Flex>
        </>
      ) : (
        /* 右页签：按新版配置更新（自动同步 selectedTag） */
        <Flex vertical gap={14}>
          {loadingConfig ? (
            <Flex align="center" justify="center" gap={8} style={{ height: 220 }}>
              <Spin size="small" />
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {isStandaloneConfigMode
                  ? t.versionPicker.loadingConfig
                  : t.projectUpdate.loadingCompose}
              </Typography.Text>
            </Flex>
          ) : !configEditable ? (
            <Alert
              type="info"
              showIcon
              title={
                configReason ||
                (isStandaloneConfigMode
                  ? t.versionPicker.configUneditable
                  : t.projectUpdate.uneditableReason)
              }
            />
          ) : !isStandaloneConfigMode ? (
            /* Case A：单容器 YAML 项目（完整操作指引 + 清空粘贴 + 恢复原配置 + 防误覆盖保护） */
            <>
              <Alert
                type="info"
                showIcon
                title={<strong>{t.projectUpdate.yamlGuideTitle}</strong>}
                description={
                  <ul
                    style={{
                      margin: '4px 0 0',
                      paddingLeft: 18,
                      fontSize: token.fontSizeSM,
                      color: token.colorTextSecondary,
                    }}
                  >
                    <li>
                      <strong style={{ color: token.colorText }}>
                        {t.projectUpdate.yamlBullet1Bold}
                      </strong>
                      {t.projectUpdate.yamlBullet1Text}
                    </li>
                    <li>
                      <strong style={{ color: token.colorText }}>
                        {t.projectUpdate.yamlBullet2Bold}
                      </strong>
                      {t.projectUpdate.yamlBullet2Before}
                      <strong style={{ color: token.colorText }}>
                        {t.projectUpdate.yamlBullet2Strong}
                      </strong>
                      {t.projectUpdate.yamlBullet2After}
                    </li>
                  </ul>
                }
              />

              <Flex wrap="wrap" align="center" justify="space-between" gap={8}>
                <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
                  {t.projectUpdate.yamlContentLabel}
                </Typography.Text>
                <Space size={8}>
                  <Button size="small" type="primary" ghost onClick={handleClearAndPaste}>
                    {t.projectUpdate.clearAndPasteBtn}
                  </Button>
                  <Button
                    size="small"
                    onClick={() => setYamlText(syncedOrigYaml)}
                    disabled={yamlText === syncedOrigYaml}
                  >
                    {t.projectUpdate.restoreOrigBtn}
                  </Button>
                </Space>
              </Flex>

              <Input.TextArea
                ref={textareaRef}
                rows={10}
                spellCheck={false}
                value={yamlText}
                onChange={(e) => setYamlText(e.target.value)}
                style={{
                  fontFamily: token.fontFamilyCode,
                  fontSize: token.fontSizeSM,
                  lineHeight: 1.55,
                }}
              />

              {overwriteRisks.missingPaths.length > 0 && (
                <Alert
                  type="warning"
                  showIcon
                  title={
                    <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                      <strong>{t.projectUpdate.warnTitle}</strong>
                      {t.projectUpdate.warnMissingPathsBefore}
                      <Typography.Text code>
                        {overwriteRisks.missingPaths.join(', ')}
                      </Typography.Text>
                      {t.projectUpdate.warnMissingPathsAfter}
                    </Typography.Text>
                  }
                />
              )}

              {overwriteRisks.missingPaths.length === 0 &&
                overwriteRisks.overwrittenSecrets.length > 0 && (
                  <Alert
                    type="warning"
                    showIcon
                    title={
                      <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                        <strong>{t.projectUpdate.warnTitle}</strong>
                        {t.projectUpdate.warnSecretOverwriteBefore}
                        <Typography.Text code>
                          {overwriteRisks.overwrittenSecrets.join(', ')}
                        </Typography.Text>
                        {t.projectUpdate.warnSecretOverwriteAfter}
                      </Typography.Text>
                    }
                  />
                )}
            </>
          ) : (
            /* Case B：无 YAML 纯独立容器（环境变量 + 端口映射表单） */
            <>
              <Alert
                type="info"
                showIcon
                title={
                  <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                    {t.versionPicker.standaloneGuide}
                  </Typography.Text>
                }
              />

              {/* 环境变量区块 */}
              <Card
                size="small"
                title={t.versionPicker.envSectionTitle}
                extra={
                  <Button
                    size="small"
                    type="primary"
                    ghost
                    icon={<PlusOutlined />}
                    onClick={() =>
                      setEnvRows((prev) => [
                        ...prev,
                        { _id: nextRowIdRef.current++, key: '', value: '' },
                      ])
                    }
                  >
                    {t.versionPicker.addEnvBtn}
                  </Button>
                }
                styles={{ body: { padding: '10px 12px' } }}
              >
                {envRows.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                    {t.versionPicker.envEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={6} style={{ maxHeight: 168, overflowY: 'auto' }}>
                    {envRows.map((row) => (
                      <Flex key={row._id} align="center" gap={6}>
                        <Input
                          size="small"
                          value={row.key}
                          placeholder={t.versionPicker.envKeyPlaceholder}
                          style={{ flex: '0 1 38%', fontFamily: token.fontFamilyCode }}
                          onChange={(e) => {
                            const val = e.target.value
                            setEnvRows((prev) =>
                              prev.map((r) => (r._id === row._id ? { ...r, key: val } : r)),
                            )
                          }}
                        />
                        <Typography.Text type="secondary">=</Typography.Text>
                        <Input
                          size="small"
                          value={row.value}
                          placeholder={t.versionPicker.envValPlaceholder}
                          style={{ flex: 1, fontFamily: token.fontFamilyCode }}
                          onChange={(e) => {
                            const val = e.target.value
                            setEnvRows((prev) =>
                              prev.map((r) => (r._id === row._id ? { ...r, value: val } : r)),
                            )
                          }}
                        />
                        <Tooltip title={t.versionPicker.removeRowTip}>
                          <Button
                            size="small"
                            type="text"
                            danger
                            icon={<DeleteOutlined />}
                            aria-label={t.versionPicker.removeRowTip}
                            onClick={() =>
                              setEnvRows((prev) => prev.filter((r) => r._id !== row._id))
                            }
                          />
                        </Tooltip>
                      </Flex>
                    ))}
                  </Flex>
                )}
              </Card>

              {/* 端口映射区块 */}
              <Card
                size="small"
                title={t.versionPicker.portsSectionTitle}
                extra={
                  <Button
                    size="small"
                    type="primary"
                    ghost
                    icon={<PlusOutlined />}
                    onClick={() =>
                      setPortRows((prev) => [
                        ...prev,
                        {
                          _id: nextRowIdRef.current++,
                          hostPort: '',
                          containerPort: '',
                          protocol: 'tcp',
                        },
                      ])
                    }
                  >
                    {t.versionPicker.addPortBtn}
                  </Button>
                }
                styles={{ body: { padding: '10px 12px' } }}
              >
                {portRows.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                    {t.versionPicker.portsEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={6} style={{ maxHeight: 148, overflowY: 'auto' }}>
                    {portRows.map((row) => (
                      <Flex key={row._id} align="center" gap={6}>
                        <Input
                          size="small"
                          value={row.hostPort}
                          placeholder={t.versionPicker.hostPortPlaceholder}
                          style={{ flex: 1, fontFamily: token.fontFamilyCode }}
                          onChange={(e) => {
                            const val = e.target.value
                            setPortRows((prev) =>
                              prev.map((r) => (r._id === row._id ? { ...r, hostPort: val } : r)),
                            )
                          }}
                        />
                        <ArrowRightOutlined
                          style={{ fontSize: 11, color: token.colorTextTertiary }}
                        />
                        <Input
                          size="small"
                          value={row.containerPort}
                          placeholder={t.versionPicker.containerPortPlaceholder}
                          style={{ flex: 1, fontFamily: token.fontFamilyCode }}
                          onChange={(e) => {
                            const val = e.target.value
                            setPortRows((prev) =>
                              prev.map((r) =>
                                r._id === row._id ? { ...r, containerPort: val } : r,
                              ),
                            )
                          }}
                        />
                        <Select
                          size="small"
                          value={(row.protocol || 'tcp').toLowerCase()}
                          style={{ width: 82 }}
                          options={[
                            { label: 'TCP', value: 'tcp' },
                            { label: 'UDP', value: 'udp' },
                          ]}
                          onChange={(val) =>
                            setPortRows((prev) =>
                              prev.map((r) => (r._id === row._id ? { ...r, protocol: val } : r)),
                            )
                          }
                        />
                        <Tooltip title={t.versionPicker.removeRowTip}>
                          <Button
                            size="small"
                            type="text"
                            danger
                            icon={<DeleteOutlined />}
                            aria-label={t.versionPicker.removeRowTip}
                            onClick={() =>
                              setPortRows((prev) => prev.filter((r) => r._id !== row._id))
                            }
                          />
                        </Tooltip>
                      </Flex>
                    ))}
                  </Flex>
                )}
              </Card>
            </>
          )}

          {/* 配置页签底栏 */}
          <Flex
            wrap="wrap"
            align="center"
            justify="space-between"
            gap={8}
            style={{
              paddingTop: 12,
              borderTop: `1px solid ${token.colorSplit}`,
            }}
          >
            <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
              {t.projectUpdate.autoRollbackNote}
            </Typography.Text>
            <Space size={8}>
              <Button onClick={onClose}>{t.versionPicker.cancel}</Button>
              <Button
                type="primary"
                disabled={
                  !configEditable ||
                  loadingConfig ||
                  (!isStandaloneConfigMode && !yamlText.trim())
                }
                loading={submittingConfig}
                onClick={handleSubmitConfig}
              >
                {t.projectUpdate.submitYamlBtn}
              </Button>
            </Space>
          </Flex>
        </Flex>
      )}
    </Flex>
  )
}
