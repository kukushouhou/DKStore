import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Button,
  Col,
  Empty,
  Flex,
  Grid,
  Input,
  Modal,
  Radio,
  Row,
  Segmented,
  Space,
  Spin,
  Switch,
  Tag,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  CheckOutlined,
  PlusOutlined,
} from '@ant-design/icons'
import {
  ApiError,
  api,
  showApiError,
  type AppCardItem,
  type ServiceDeviceItem,
  type ServiceEnvItem,
  type ServiceItem,
  type ServicePortItem,
  type ServiceTagItem,
  type ServiceVolumeItem,
  type SuggestedComposeDir,
  type TagSortMode,
  type UpdateCandidates,
  type UpdatePolicyMode,
  type UpdateServiceConfigPayload,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import { detectComposeOverwriteRisks, replaceComposeImageTag } from './composeGuard'
import ComposeSaveLocationBar, { type ComposeSaveMode } from './ComposeSaveLocationBar'
import ConfigVisualEditor from './ConfigVisualEditor'
import {
  buildDockerRunCommand,
  cloneEditableConfig,
  editableConfigFromApi,
  normalizeNetMode,
  normalizeRestart,
  parseComposeServices,
  parseDockerRunCommand,
  patchComposeServiceInYaml,
  type EditableServiceConfig,
} from './configSync'
import DeleteAppModal from './DeleteAppModal'
import LockableVersionBar from './LockableVersionBar'
import StoppedUpdateModal from './StoppedUpdateModal'
import { compareNumericSegs, isPrereleaseTagStr, parseNumericSegs } from './versionUtils'

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

function replaceImageTagInRef(imageRef: string, newTag: string): string {
  const cleanRef = imageRef.trim().split('@')[0]
  const lastSlash = cleanRef.lastIndexOf('/')
  const colonIdx = cleanRef.indexOf(':', lastSlash + 1)
  const base = colonIdx >= 0 ? cleanRef.slice(0, colonIdx) : cleanRef
  return newTag ? `${base}:${newTag}` : base
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

function stripDisplayHash(raw?: string): string {
  if (!raw) return ''
  return raw.trim().split(' (')[0].trim()
}

function extractTagFromRef(imageRef?: string): string {
  if (!imageRef) return ''
  const withoutDigest = imageRef.trim().split('@')[0]
  const colonIdx = withoutDigest.lastIndexOf(':')
  const slashIdx = withoutDigest.lastIndexOf('/')
  if (colonIdx > slashIdx) {
    return withoutDigest.slice(colonIdx + 1).trim()
  }
  return ''
}

function cleanInitialTag(svc: ServiceItem, explicit?: string): string {
  const expClean = stripDisplayHash(explicit)
  if (expClean) return expClean
  const targetRefTag = extractTagFromRef(svc.targetRef)
  if (targetRefTag) return targetRefTag
  if (svc.rawTargetVersion && !svc.rawTargetVersion.includes(' ')) {
    return svc.rawTargetVersion.trim()
  }
  if (svc.targetVersion && !svc.targetVersion.includes(' ')) {
    return svc.targetVersion.trim()
  }
  const curRefTag = extractTagFromRef(svc.imageRef)
  if (curRefTag) return curRefTag
  const curClean = stripDisplayHash(svc.currentVersion)
  if (curClean) return curClean
  return 'latest'
}

function parseAnyNumericSegs(tag?: string): number[] {
  if (!tag) return []
  const clean = tag.trim().replace(/^v/i, '').split(/[-+_]/)[0]
  if (!/^\d+(\.\d+)*$/.test(clean)) return []
  const nums = clean.split('.').map((p) => Number(p))
  if (nums.length === 1 && nums[0] > 1000) return []
  return nums
}

function compareAnyNumericSegs(tagA?: string, tagB?: string): number | null {
  const a = parseAnyNumericSegs(tagA)
  const b = parseAnyNumericSegs(tagB)
  if (a.length === 0 || b.length === 0) return null
  if (a.length >= 2 && b.length >= 2) {
    return compareNumericSegs(tagA, tagB)
  }
  const len = Math.max(a.length, b.length)
  for (let i = 0; i < len; i++) {
    const va = a[i] ?? 0
    const vb = b[i] ?? 0
    if (va !== vb) return va - vb
  }
  const preA = isPrereleaseTagStr(tagA)
  const preB = isPrereleaseTagStr(tagB)
  if (preA !== preB) return preA ? -1 : 1
  return 0
}

const PAGE_RENDER_STEP = 80

function sortMatchedTagsClient(
  items: ServiceTagItem[],
  rawQuery: string,
  sortMode: TagSortMode,
  hasTimestamps: boolean,
): ServiceTagItem[] {
  const qLow = rawQuery.trim().toLowerCase()
  const trimV = (s: string) => s.replace(/^v/i, '')
  const qNoV = trimV(qLow)
  return [...items].sort((a, b) => {
    const la = a.tag.toLowerCase()
    const lb = b.tag.toLowerCase()
    const exactA = la === qLow || (qNoV !== '' && trimV(la) === qNoV)
    const exactB = lb === qLow || (qNoV !== '' && trimV(lb) === qNoV)
    if (exactA !== exactB) return exactA ? -1 : 1
    const prefA = la.startsWith(qLow) || (qNoV !== '' && trimV(la).startsWith(qNoV))
    const prefB = lb.startsWith(qLow) || (qNoV !== '' && trimV(lb).startsWith(qNoV))
    if (prefA !== prefB) return prefA ? -1 : 1
    if (sortMode === 'time' && hasTimestamps && (a.updatedAt ?? 0) !== (b.updatedAt ?? 0)) {
      return (b.updatedAt ?? 0) - (a.updatedAt ?? 0)
    }
    if (
      a.searchOrder !== undefined &&
      b.searchOrder !== undefined &&
      a.searchOrder !== b.searchOrder
    ) {
      return a.searchOrder - b.searchOrder
    }
    const cmp = compareAnyNumericSegs(b.tag, a.tag)
    if (cmp !== null) {
      if (cmp !== 0) return cmp
      if (a.tag.length !== b.tag.length) return a.tag.length - b.tag.length
    }
    if ((a.updatedAt ?? 0) !== (b.updatedAt ?? 0)) {
      return (b.updatedAt ?? 0) - (a.updatedAt ?? 0)
    }
    return b.tag.localeCompare(a.tag)
  })
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
      width={screens.md ? 880 : '95vw'}
      style={{ maxWidth: '94vw' }}
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
  const userPickedVersionRef = useRef<boolean>(false)

  const canShowConfigTab = Boolean(!onPickOnly && app && app.source !== 'vendor_managed')
  const [tab, setTab] = useState<'version' | 'config'>(
    canShowConfigTab ? initialTab : 'version',
  )

  const initialPrereleaseOn = useMemo(
    () =>
      isPrereleaseTagStr(extractTagFromRef(service.imageRef)) ||
      isPrereleaseTagStr(service.currentVersion) ||
      isPrereleaseTagStr(initialSelectedTag),
    [initialSelectedTag, service.currentVersion, service.imageRef],
  )

  const [allTags, setAllTags] = useState<ServiceTagItem[]>([])
  const [candidates, setCandidates] = useState<UpdateCandidates | undefined>(undefined)
  const [highestStableTag, setHighestStableTag] = useState<string>('')
  const [highestPrereleaseTag, setHighestPrereleaseTag] = useState<string>('')
  const [loading, setLoading] = useState(true)
  const [showPrerelease, setShowPrerelease] = useState<boolean>(initialPrereleaseOn)
  const [sortMode, setSortMode] = useState<TagSortMode>('version')
  const [hasTimestamps, setHasTimestamps] = useState<boolean>(true)
  const [query, setQuery] = useState('')
  const [visibleLimit, setVisibleLimit] = useState<number>(PAGE_RENDER_STEP)
  const [selectedTag, setSelectedTag] = useState(() =>
    cleanInitialTag(service, initialSelectedTag),
  )
  const [policy, setPolicy] = useState<UpdatePolicyMode>('all')
  const [lockDepth, setLockDepth] = useState(1)
  const [submitting, setSubmitting] = useState(false)

  const showPrereleaseRef = useRef(showPrerelease)
  showPrereleaseRef.current = showPrerelease
  const userToggledPreRef = useRef(false)
  const listBoxRef = useRef<HTMLDivElement | null>(null)

  // 按配置更新 —— 分支 A：单容器 YAML 项目编辑状态
  const isStandaloneConfigMode = Boolean(
    app && !app.composeEditable && app.source === 'standalone',
  )
  const [yamlLoaded, setYamlLoaded] = useState(false)
  const [yamlEditable, setYamlEditable] = useState(true)
  const [yamlReason, setYamlReason] = useState('')
  const [origYaml, setOrigYaml] = useState('')
  const [yamlText, setYamlText] = useState('')
  const [yamlComposePath, setYamlComposePath] = useState('')
  const [yamlProjectName, setYamlProjectName] = useState('')
  const [yamlHostName, setYamlHostName] = useState('')
  const [yamlPathMissing, setYamlPathMissing] = useState(false)
  const [yamlPathReadOnly, setYamlPathReadOnly] = useState(false)
  const [yamlOrigWritable, setYamlOrigWritable] = useState(true)
  const [yamlSuggestedDirs, setYamlSuggestedDirs] = useState<SuggestedComposeDir[]>([])
  const [yamlSaveMode, setYamlSaveMode] = useState<ComposeSaveMode>('orig')
  const [yamlPickedNewFile, setYamlPickedNewFile] = useState('')
  const [effectiveOrigServices, setEffectiveOrigServices] = useState<string[]>([])
  const [extraFileServices, setExtraFileServices] = useState<string[]>([])
  const [pendingRemovalConfirm, setPendingRemovalConfirm] = useState<{
    removedServices: string[]
    addedServices: string[]
  } | null>(null)

  // 按配置更新 —— 分支 B：独立容器命令模式（docker run <-> 可视化表单实时双向同步）
  const [standaloneLoaded, setStandaloneLoaded] = useState(false)
  const [standaloneEditable, setStandaloneEditable] = useState(true)
  const [standaloneReason, setStandaloneReason] = useState('')
  const [origCmdCfg, setOrigCmdCfg] = useState<EditableServiceConfig | null>(null)
  const [origCmdText, setOrigCmdText] = useState('')
  const [cmdCfg, setCmdCfg] = useState<EditableServiceConfig | null>(null)
  const [cmdText, setCmdText] = useState('')

  const [submittingConfig, setSubmittingConfig] = useState(false)
  const [stoppedPending, setStoppedPending] = useState<
    | { kind: 'version' }
    | {
        kind: 'yaml'
        opts?: {
          confirmRemoveServices?: boolean
          deleteRemovedImages?: boolean
          deleteRemovedFileIds?: string[]
        }
      }
    | {
        kind: 'standalone'
        payload: Omit<UpdateServiceConfigPayload, 'startAfter'>
      }
    | null
  >(null)
  const [stoppedChoiceLoading, setStoppedChoiceLoading] = useState<'keep' | 'start' | null>(null)
  const isSvcStopped = Boolean(
    service.state && service.state.trim() && service.state !== 'running',
  )

  const defaultTrackTag = useMemo(
    () => extractTagFromRef(service.targetRef) || extractTagFromRef(service.imageRef) || 'latest',
    [service.imageRef, service.targetRef],
  )

  const resolveHighestForTag = (
    tag: string,
    pool: ServiceTagItem[],
    stHi: string,
    preHi: string,
  ): string => {
    const isTagPre = Boolean(
      pool.find((i) => i.tag === tag)?.prerelease || isPrereleaseTagStr(tag),
    )
    if (isTagPre && preHi) return preHi
    if (stHi) return stHi
    if (preHi) return preHi
    if (service.rawTargetVersion && !service.rawTargetVersion.includes(' ')) {
      return service.rawTargetVersion
    }
    if (service.targetVersion && !service.targetVersion.includes(' ')) {
      return service.targetVersion
    }
    const firstVer = pool.find((item) => item.tag !== 'latest')
    if (firstVer) return firstVer.tag
    return pool[0]?.tag || ''
  }

  // 计算当前所选标签对应通道（稳定版 / 预发布版）内已知最高版本标签
  const highestTag = useMemo(
    () => resolveHighestForTag(selectedTag, allTags, highestStableTag, highestPrereleaseTag),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [allTags, highestPrereleaseTag, highestStableTag, selectedTag, service],
  )

  const checkIsAtOrAboveLatest = (
    tag: string,
    knownHighest: string,
    pool: ServiceTagItem[],
  ): boolean => {
    if (!knownHighest || tag === knownHighest || tag === 'latest') return true
    if (isPrereleaseTagStr(tag) && parseAnyNumericSegs(tag).length === 0) return true
    if (
      tag === defaultTrackTag &&
      (service.kind === 'digest_update' || service.kind === 'none')
    ) {
      return true
    }
    const tagEntry = pool.find((item) => item.tag === tag)
    const hiEntry = pool.find((item) => item.tag === knownHighest)
    if (tagEntry?.digest && hiEntry?.digest && tagEntry.digest === hiEntry.digest) {
      return true
    }
    const cmp = compareAnyNumericSegs(tag, knownHighest)
    if (cmp !== null && cmp >= 0) {
      return true
    }
    return false
  }

  const applyTagSelection = (tag: string) => {
    userPickedVersionRef.current = true
    setSelectedTag(tag)
    setLockDepth(1)
    const knownHighest = resolveHighestForTag(tag, allTags, highestStableTag, highestPrereleaseTag)
    const atLatest = checkIsAtOrAboveLatest(tag, knownHighest, allTags)
    setPolicy(atLatest ? 'all' : 'skip_latest')

    if (yamlLoaded && yamlEditable) {
      setYamlText((prev) => replaceComposeImageTag(prev, service.imageRef, tag))
    }
    if (standaloneLoaded && standaloneEditable && cmdCfg) {
      const nextCfg = cloneEditableConfig(cmdCfg)
      nextCfg.image = replaceImageTagInRef(nextCfg.image || service.imageRef, tag)
      setCmdCfg(nextCfg)
      setCmdText(buildDockerRunCommand(nextCfg))
    }
  }

  const resetListViewport = () => {
    setVisibleLimit(PAGE_RENDER_STEP)
    if (listBoxRef.current) {
      listBoxRef.current.scrollTop = 0
    }
  }

  const handleTogglePrerelease = (checked: boolean) => {
    userToggledPreRef.current = true
    setShowPrerelease(checked)
    resetListViewport()
  }

  const handleChangeSortMode = (nextSort: TagSortMode) => {
    if (nextSort === 'time' && !hasTimestamps) return
    setSortMode(nextSort)
    resetListViewport()
  }

  const tagsLoadedForKeyRef = useRef<string>('')
  useEffect(() => {
    if (tab !== 'version') {
      setLoading(false)
      return
    }
    const fetchKey = `${service.key}::${initialSelectedTag ?? ''}`
    if (tagsLoadedForKeyRef.current === fetchKey) {
      return
    }
    tagsLoadedForKeyRef.current = fetchKey

    let cancelled = false
    const initTag = cleanInitialTag(service, initialSelectedTag)
    setLoading(true)

    ;(async () => {
      try {
        const res = await api.searchServiceTags(service.key, {
          all: true,
          remote: true,
        })
        if (cancelled) return
        if (res.currentPrerelease && !userToggledPreRef.current && !showPrereleaseRef.current) {
          setShowPrerelease(true)
        }
        if (res.hasTimestamps !== undefined) {
          setHasTimestamps(res.hasTimestamps)
        }
        const items = res.items ?? []
        const stHi = res.highestStableTag || res.highestTag || ''
        const preHi = res.highestPrereleaseTag || res.highestTag || ''
        setAllTags(items)
        setCandidates(res.candidates)
        setHighestStableTag(stHi)
        setHighestPrereleaseTag(preHi)

        if (!userPickedVersionRef.current) {
          const effHi = resolveHighestForTag(initTag, items, stHi, preHi)
          const atLatest = checkIsAtOrAboveLatest(initTag, effHi, items)
          setSelectedTag(initTag)
          setPolicy(atLatest ? 'all' : 'skip_latest')
        }
      } catch (e) {
        if (!cancelled) {
          setAllTags([])
          setCandidates(undefined)
          showApiError(e)
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()

    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [service, initialSelectedTag, tab])

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
        setYamlComposePath(res.composePath || '')
        setYamlProjectName(res.projectName || app.projectName || app.name)
        setYamlHostName(res.hostName || app.hostName)
        const pMissing = Boolean(res.pathMissing)
        const pReadOnly = Boolean(res.pathReadOnly)
        const oWritable = res.origWritable !== false && Boolean(res.composePath)
        setYamlPathMissing(pMissing)
        setYamlPathReadOnly(pReadOnly)
        setYamlOrigWritable(oWritable)
        setYamlSuggestedDirs(res.suggestedDirs ?? [])
        setYamlSaveMode((pMissing || pReadOnly) && !oWritable ? 'new' : 'orig')
        setEffectiveOrigServices(res.effectiveServices ?? [])
        setExtraFileServices(res.extraFileServices ?? [])
        const initialYaml = userPickedVersionRef.current
          ? replaceComposeImageTag(raw, service.imageRef, selectedTag)
          : raw
        setYamlText(initialYaml)
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

  // 加载独立容器命令配置（Case B）
  useEffect(() => {
    if (tab !== 'config' || !isStandaloneConfigMode || standaloneLoaded) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.getServiceConfig(service.key)
        if (cancelled) return
        setStandaloneEditable(res.editable)
        setStandaloneReason(res.reason || '')
        const baseOrigCfg = editableConfigFromApi(
          res,
          service.name && service.name !== 'main' ? service.name : appName,
          res.imageRef || service.imageRef,
        )
        const baseOrigCmd = buildDockerRunCommand(baseOrigCfg)
        setOrigCmdCfg(baseOrigCfg)
        setOrigCmdText(baseOrigCmd)

        if (userPickedVersionRef.current && selectedTag) {
          const pickedCfg = cloneEditableConfig(baseOrigCfg)
          pickedCfg.image = replaceImageTagInRef(baseOrigCfg.image, selectedTag)
          setCmdCfg(pickedCfg)
          setCmdText(buildDockerRunCommand(pickedCfg))
        } else {
          setCmdCfg(cloneEditableConfig(baseOrigCfg))
          setCmdText(baseOrigCmd)
        }
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
  }, [tab, isStandaloneConfigMode, standaloneLoaded, service.key, service.name, service.imageRef, appName, selectedTag])

  const handleApplySearch = () => {
    resetListViewport()
  }

  const trimmedQuery = query.trim()
  const effectiveSortMode: TagSortMode = hasTimestamps ? sortMode : 'version'

  const matchedAllTags = useMemo(() => {
    if (trimmedQuery) {
      const qLow = trimmedQuery.toLowerCase()
      const rawMatched = allTags.filter((item) => item.tag.toLowerCase().includes(qLow))
      return sortMatchedTagsClient(rawMatched, trimmedQuery, effectiveSortMode, hasTimestamps)
    }

    const base = allTags.filter((item) =>
      showPrerelease ? Boolean(item.preVisible) : Boolean(item.stableVisible),
    )

    const initTag = cleanInitialTag(service, initialSelectedTag)
    const initIsPre = isPrereleaseTagStr(initTag)
    let list = base
    if (initTag && (!initIsPre || showPrerelease) && !base.some((item) => item.tag === initTag)) {
      const existingInPool = allTags.find((item) => item.tag === initTag)
      const extraItem: ServiceTagItem = existingInPool ?? {
        tag: initTag,
        updatedAt: service.targetUpdatedAt || service.currentUpdatedAt,
        prerelease: initIsPre,
      }
      if (effectiveSortMode === 'version' && base[0]?.tag === 'latest') {
        list = [base[0], extraItem, ...base.slice(1)]
      } else {
        list = [extraItem, ...base]
      }
    }

    if (effectiveSortMode === 'time') {
      return [...list].sort((a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0))
    }
    return list
  }, [
    allTags,
    effectiveSortMode,
    hasTimestamps,
    initialSelectedTag,
    service,
    showPrerelease,
    trimmedQuery,
  ])

  const displayedTags = useMemo(
    () => matchedAllTags.slice(0, visibleLimit),
    [matchedAllTags, visibleLimit],
  )

  const candidateTracks = useMemo(() => {
    if (!candidates) return []
    const defs: {
      kind: 'patch' | 'minor' | 'major'
      tag?: string
      label: string
      tip: string
      color: 'cyan' | 'blue' | 'purple'
    }[] = [
      {
        kind: 'patch',
        tag: candidates.patch,
        label: t.versionPicker.candPatch,
        tip: t.versionPicker.candPatchTip,
        color: 'cyan',
      },
      {
        kind: 'minor',
        tag: candidates.minor,
        label: t.versionPicker.candMinor,
        tip: t.versionPicker.candMinorTip,
        color: 'blue',
      },
      {
        kind: 'major',
        tag: candidates.major,
        label: t.versionPicker.candMajor,
        tip: t.versionPicker.candMajorTip,
        color: 'purple',
      },
    ]
    const seen = new Set<string>()
    const out: {
      kind: 'patch' | 'minor' | 'major'
      tag: string
      label: string
      tip: string
      color: 'cyan' | 'blue' | 'purple'
    }[] = []
    for (const d of defs) {
      const tg = (d.tag || '').trim()
      if (!tg || seen.has(tg)) continue
      if (!showPrerelease && isPrereleaseTagStr(tg)) continue
      seen.add(tg)
      out.push({ ...d, tag: tg })
    }
    return out.length >= 2 ? out : []
  }, [
    candidates,
    showPrerelease,
    t.versionPicker.candMajor,
    t.versionPicker.candMajorTip,
    t.versionPicker.candMinor,
    t.versionPicker.candMinorTip,
    t.versionPicker.candPatch,
    t.versionPicker.candPatchTip,
  ])

  const candidateByTag = useMemo(
    () => new Map(candidateTracks.map((c) => [c.tag, c])),
    [candidateTracks],
  )

  const handlePickCandidateTrack = (tag: string) => {
    if (trimmedQuery && !tag.toLowerCase().includes(trimmedQuery.toLowerCase())) {
      setQuery('')
    }
    applyTagSelection(tag)
    const idx = matchedAllTags.findIndex((item) => item.tag === tag)
    if (idx >= visibleLimit) {
      setVisibleLimit(idx + PAGE_RENDER_STEP)
    }
    requestAnimationFrame(() => {
      const box = listBoxRef.current
      if (!box) return
      const esc =
        typeof CSS !== 'undefined' && typeof CSS.escape === 'function'
          ? CSS.escape(tag)
          : tag.replace(/["\\]/g, '\\$&')
      const el = box.querySelector<HTMLElement>(`[data-tag-option="${esc}"]`)
      el?.scrollIntoView({ block: 'nearest' })
    })
  }

  const handleListScroll = (e: React.UIEvent<HTMLDivElement>) => {
    const el = e.currentTarget
    if (
      visibleLimit < matchedAllTags.length &&
      el.scrollTop + el.clientHeight >= el.scrollHeight - 120
    ) {
      setVisibleLimit((prev) => Math.min(prev + PAGE_RENDER_STEP, matchedAllTags.length))
    }
  }

  const isSelectedPrerelease = useMemo(
    () =>
      Boolean(
        allTags.find((i) => i.tag === selectedTag)?.prerelease || isPrereleaseTagStr(selectedTag),
      ),
    [allTags, selectedTag],
  )
  const isLatest = checkIsAtOrAboveLatest(selectedTag, highestTag, allTags)
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
      const hit = allTags.find((r) => {
        if (r.tag === selectedTag) return false
        if (!isSelectedPrerelease && (r.prerelease || isPrereleaseTagStr(r.tag))) return false
        const clean = r.tag.replace(/^v/i, '')
        const matchesPrefix = clean === lockedPrefix || clean.startsWith(`${lockedPrefix}.`)
        if (!matchesPrefix) return false
        return compareNumericSegs(r.tag, selectedTag) > 0
      })
      if (hit) return hit.tag
    }
    return ''
  }, [allTags, highestTag, isLatest, isSelectedPrerelease, lockedPrefix, policy, selectedTag])

  const origYamlConfigs = useMemo(() => parseComposeServices(origYaml), [origYaml])
  const curYamlConfigs = useMemo(() => parseComposeServices(yamlText), [yamlText])

  const overwriteRisks = useMemo(
    () =>
      detectComposeOverwriteRisks(origYaml, yamlText, {
        effectiveOrigServices,
        extraFileServices,
      }),
    [effectiveOrigServices, extraFileServices, origYaml, yamlText],
  )

  const doSubmitVersion = async (startAfter?: boolean) => {
    if (!service || !selectedTag) return
    const selection: VersionPickSelection = {
      targetTag: selectedTag,
      policyMode: policy,
      policyPrefix: policy === 'prefix' ? lockedPrefix : undefined,
      skipTargetTag: policy === 'skip_latest' ? highestTag : undefined,
    }
    setSubmitting(true)
    if (startAfter !== undefined) {
      setStoppedChoiceLoading(startAfter ? 'start' : 'keep')
    }
    try {
      const res = await api.startUpdates({
        items: [
          {
            serviceKey: service.key,
            targetTag: selection.targetTag,
            policyMode: selection.policyMode,
            policyPrefix: selection.policyPrefix,
            skipTargetTag: selection.skipTargetTag,
            startAfter,
          },
        ],
      })
      if (res.items) {
        for (const job of res.items) {
          upsertUpdateJob(job)
        }
      }
      message.info(t.home.updating)
      setStoppedPending(null)
      onUpdated?.()
      onClose()
    } catch (e) {
      showApiError(e)
    } finally {
      setSubmitting(false)
      setStoppedChoiceLoading(null)
    }
  }

  const handleSubmitVersion = () => {
    if (!service || !selectedTag) return
    if (onPickOnly) {
      onPickOnly({
        targetTag: selectedTag,
        policyMode: policy,
        policyPrefix: policy === 'prefix' ? lockedPrefix : undefined,
        skipTargetTag: policy === 'skip_latest' ? highestTag : undefined,
      })
      onClose()
      return
    }
    if (isSvcStopped) {
      setStoppedPending({ kind: 'version' })
      return
    }
    void doSubmitVersion()
  }

  const needYamlSaveChoice = !isStandaloneConfigMode && (yamlPathMissing || yamlPathReadOnly)
  const effectiveYamlTargetFile = needYamlSaveChoice
    ? yamlSaveMode === 'orig'
      ? yamlOrigWritable
        ? yamlComposePath
        : ''
      : yamlPickedNewFile
    : ''

  const submitYamlConfigWithOptions = async (opts?: {
    confirmRemoveServices?: boolean
    deleteRemovedImages?: boolean
    deleteRemovedFileIds?: string[]
    startAfter?: boolean
  }) => {
    if (!app) return
    const policyPrefixVal = policy === 'prefix' ? lockedPrefix : undefined
    const skipTargetVal = policy === 'skip_latest' ? highestTag : undefined
    const res = await api.updateAppCompose({
      appKey: app.key,
      content: yamlText,
      targetComposePath: needYamlSaveChoice ? effectiveYamlTargetFile || undefined : undefined,
      serviceKey: service.key,
      policyMode: policy,
      policyPrefix: policyPrefixVal,
      skipTargetTag: skipTargetVal,
      confirmRemoveServices: opts?.confirmRemoveServices,
      deleteRemovedImages: opts?.deleteRemovedImages,
      deleteRemovedFileIds: opts?.deleteRemovedFileIds,
      startAfter: opts?.startAfter,
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
    setStoppedPending(null)
    onUpdated?.()
    onClose()
  }

  const executeYamlConfigOrPrompt = async (opts?: {
    confirmRemoveServices?: boolean
    deleteRemovedImages?: boolean
    deleteRemovedFileIds?: string[]
  }) => {
    if (isSvcStopped) {
      setStoppedPending({ kind: 'yaml', opts })
      return
    }
    setSubmittingConfig(true)
    try {
      await submitYamlConfigWithOptions(opts)
    } catch (e) {
      if (e instanceof ApiError && e.code === 'compose_services_removed' && e.removedServices?.length) {
        setPendingRemovalConfirm({
          removedServices: e.removedServices,
          addedServices: e.addedServices ?? [],
        })
        return
      }
      showApiError(e)
    } finally {
      setSubmittingConfig(false)
    }
  }

  const doStandaloneSubmit = async (
    basePayload: Omit<UpdateServiceConfigPayload, 'startAfter'>,
    startAfter?: boolean,
  ) => {
    setSubmittingConfig(true)
    if (startAfter !== undefined) {
      setStoppedChoiceLoading(startAfter ? 'start' : 'keep')
    }
    try {
      const res = await api.updateServiceConfig({
        ...basePayload,
        startAfter,
      })
      if (res.job) {
        upsertUpdateJob(res.job)
      }
      message.success(t.versionPicker.configUpdateStarted)
      setStoppedPending(null)
      onUpdated?.()
      onClose()
    } catch (e) {
      showApiError(e)
    } finally {
      setSubmittingConfig(false)
      setStoppedChoiceLoading(null)
    }
  }

  const handleSubmitConfig = async () => {
    if (!app) return

    if (!isStandaloneConfigMode) {
      if (!yamlText.trim()) {
        message.warning(t.projectUpdate.yamlEmptyError)
        return
      }
      if (overwriteRisks.missingServices.length > 0) {
        setPendingRemovalConfirm({
          removedServices: overwriteRisks.missingServices,
          addedServices: overwriteRisks.addedServices,
        })
        return
      }
      await executeYamlConfigOrPrompt()
      return
    }

    // 分支 B：独立容器命令模式提交
    if (!cmdCfg) return

    const cleanedEnv: ServiceEnvItem[] = []
    for (const row of cmdCfg.envs) {
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
    for (const row of cmdCfg.ports) {
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

    const cleanedVolumes: ServiceVolumeItem[] = []
    for (const row of cmdCfg.volumes) {
      const hp = row.hostPath.trim()
      const cp = row.containerPath.trim()
      if (!hp && !cp) continue
      if (!cp || !cp.startsWith('/')) {
        message.warning(t.configEditor.volInvalidMsg)
        return
      }
      cleanedVolumes.push({
        hostPath: hp,
        containerPath: cp,
        mode: row.mode || 'rw',
      })
    }

    const cleanedDevices: ServiceDeviceItem[] = []
    for (const row of cmdCfg.devices) {
      const hp = row.hostPath.trim()
      const cp = (row.containerPath || row.hostPath).trim()
      if (!hp && !cp) continue
      cleanedDevices.push({
        hostPath: hp || cp,
        containerPath: cp || hp,
        ...(row.permissions ? { permissions: row.permissions } : {}),
      })
    }

    const targetImg = cmdCfg.image.trim() || origCmdCfg?.image || service.imageRef
    const targetTagVal = extractTagFromRef(targetImg) || selectedTag
    const policyPrefixVal = policy === 'prefix' ? lockedPrefix : undefined
    const skipTargetVal = policy === 'skip_latest' ? highestTag : undefined

    const basePayload: Omit<UpdateServiceConfigPayload, 'startAfter'> = {
      serviceKey: service.key,
      targetTag: targetTagVal,
      targetImageRef: targetImg,
      policyMode: policy,
      policyPrefix: policyPrefixVal,
      skipTargetTag: skipTargetVal,
      env: cleanedEnv,
      ports: cleanedPorts,
      volumes: cleanedVolumes,
      devices: cleanedDevices,
      networkMode: normalizeNetMode(cmdCfg.networkMode),
      restartPolicy: normalizeRestart(cmdCfg.restartPolicy) || 'no',
      user: cmdCfg.user.trim(),
      privileged: Boolean(cmdCfg.privileged),
      capAdd: cmdCfg.capAdd,
      hostname: cmdCfg.hostname.trim(),
      extraHosts: cmdCfg.extraHosts,
    }

    if (isSvcStopped) {
      setStoppedPending({ kind: 'standalone', payload: basePayload })
      return
    }
    await doStandaloneSubmit(basePayload)
  }

  const shortRepo = extractShortRepo(service.imageRef)
  const curVer = service.currentVersion || t.home.unknownCurrent
  const exactTagExists = allTags.some((r) => r.tag === trimmedQuery)

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
      <div
        style={{
          paddingBottom: 14,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <Flex wrap="wrap" align="flex-start" justify="space-between" gap={12}>
          <div style={{ minWidth: 0, flex: 1 }}>
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

          {/* 右上角双页签切换：选择版本 | 按配置更新 */}
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

        {/* 目标镜像胶囊（仅在选择版本页签展示；按配置更新页签直接在命令/YAML中编辑） */}
        {tab === 'version' && (
          <div style={{ marginTop: 10 }}>
            <Tag
              variant="filled"
              color="processing"
              style={{
                margin: 0,
                padding: '3px 10px',
                display: 'inline-flex',
                flexWrap: 'wrap',
                alignItems: 'baseline',
                gap: 6,
                maxWidth: '100%',
                whiteSpace: 'normal',
              }}
            >
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}>
                {t.versionPicker.targetImage}
              </Typography.Text>
              <span
                style={{
                  fontFamily: token.fontFamilyCode,
                  fontSize: token.fontSizeSM,
                  fontWeight: 600,
                  color: token.colorPrimary,
                  wordBreak: 'break-all',
                }}
              >
                {shortRepo}:{selectedTag}
              </span>
            </Tag>
          </div>
        )}
      </div>

      {tab === 'version' ? (
        <>
          {/* 双栏主体：桌面左宽右精 14:10，移动端上下叠放 */}
          <Row gutter={[20, 16]} align="stretch">
            <Col xs={24} md={14} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              {/* 组合式搜索框（直接基于已拉取的完整标签池本地即时筛选，点击按钮或回车同样本地生效） */}
              <Space.Compact style={{ width: '100%' }}>
                <Input
                  allowClear
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value)
                    resetListViewport()
                  }}
                  onPressEnter={(e) => {
                    e.preventDefault()
                    handleApplySearch()
                  }}
                  placeholder={t.versionPicker.searchPlaceholder}
                  style={{ fontFamily: token.fontFamilyCode }}
                />
                <Button type="primary" ghost onClick={handleApplySearch}>
                  {t.versionPicker.searchBtn}
                </Button>
              </Space.Compact>

              {/* 排序方式切换 + 预发布版本开关 */}
              <Flex align="center" justify="space-between" gap={8} wrap="wrap">
                <Segmented
                  size="small"
                  value={effectiveSortMode}
                  onChange={(val) => handleChangeSortMode(val as TagSortMode)}
                  options={[
                    { label: t.versionPicker.sortByVersion, value: 'version' },
                    {
                      label: hasTimestamps ? (
                        t.versionPicker.sortByTime
                      ) : (
                        <Tooltip title={t.versionPicker.sortByTimeDisabledTip}>
                          <span>{t.versionPicker.sortByTime}</span>
                        </Tooltip>
                      ),
                      value: 'time',
                      disabled: !hasTimestamps,
                    },
                  ]}
                />
                <Tooltip title={t.versionPicker.prereleaseSwitchTip}>
                  <Space
                    size={6}
                    style={{ cursor: 'pointer', paddingInline: 2 }}
                    onClick={() => handleTogglePrerelease(!showPrerelease)}
                  >
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: token.fontSizeSM, userSelect: 'none' }}
                    >
                      {t.versionPicker.prereleaseSwitch}
                    </Typography.Text>
                    <Switch
                      size="small"
                      checked={showPrerelease}
                      onChange={(checked, e) => {
                        e.stopPropagation()
                        handleTogglePrerelease(checked)
                      }}
                    />
                  </Space>
                </Tooltip>
              </Flex>

              {trimmedQuery && (
                <Typography.Text
                  type="secondary"
                  style={{ fontSize: token.fontSizeSM, paddingInline: 2 }}
                >
                  {t.versionPicker.matchedCaption.replace('{q}', trimmedQuery)}
                </Typography.Text>
              )}

              {/* 标签列表：桌面双栏下由外层弹性壳吸收右栏撑出的全部剩余高度，避免列表下方留白且不反向撑高右栏 */}
              <div
                style={{
                  flex: screens.md ? '1 1 260px' : undefined,
                  minHeight: screens.md ? 220 : undefined,
                  position: screens.md ? 'relative' : 'static',
                }}
              >
                <div
                  ref={listBoxRef}
                  role="listbox"
                  onScroll={handleListScroll}
                  style={{
                    position: screens.md ? 'absolute' : 'static',
                    inset: screens.md ? 0 : undefined,
                    maxHeight: screens.md ? undefined : 260,
                    minHeight: screens.md ? undefined : 180,
                    overflowY: 'auto',
                    borderRadius: token.borderRadiusLG,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    background: token.colorBgContainer,
                  }}
                >
                {loading ? (
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
                      const isPre = Boolean(item.prerelease || isPrereleaseTagStr(item.tag))
                      const candBadge = candidateByTag.get(item.tag)
                      return (
                        <Flex
                          key={item.tag}
                          data-tag-option={item.tag}
                          role="option"
                          aria-selected={active}
                          tabIndex={0}
                          align="flex-start"
                          justify="space-between"
                          gap={10}
                          onClick={() => applyTagSelection(item.tag)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault()
                              applyTagSelection(item.tag)
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
                          <Flex align="flex-start" gap={6} style={{ minWidth: 0, flex: 1 }}>
                            <CheckOutlined
                              style={{
                                fontSize: 12,
                                marginTop: 4,
                                color: token.colorPrimary,
                                visibility: active ? 'visible' : 'hidden',
                                flexShrink: 0,
                              }}
                            />
                            <div
                              style={{
                                display: 'flex',
                                flexWrap: 'wrap',
                                alignItems: 'center',
                                columnGap: 6,
                                rowGap: 2,
                                minWidth: 0,
                                flex: 1,
                              }}
                            >
                              <span
                                style={{
                                  wordBreak: 'break-all',
                                  fontFamily: token.fontFamilyCode,
                                  fontSize: token.fontSizeSM,
                                  lineHeight: '20px',
                                  fontWeight: active ? 600 : 400,
                                  color: active ? token.colorPrimary : token.colorText,
                                }}
                              >
                                {item.tag}
                                {item.aliasOf && (
                                  <span
                                    style={{
                                      marginLeft: 5,
                                      fontSize: 11,
                                      fontWeight: 400,
                                      color: token.colorTextSecondary,
                                    }}
                                  >
                                    ({item.aliasOf})
                                  </span>
                                )}
                              </span>
                              {candBadge && (
                                <Tag
                                  color={candBadge.color}
                                  style={{
                                    margin: 0,
                                    fontSize: 11,
                                    lineHeight: '16px',
                                    paddingInline: 5,
                                    flexShrink: 0,
                                  }}
                                >
                                  {candBadge.label}
                                </Tag>
                              )}
                              {isPre && (
                                <Tag
                                  color="warning"
                                  style={{
                                    margin: 0,
                                    fontSize: 11,
                                    lineHeight: '16px',
                                    paddingInline: 5,
                                    flexShrink: 0,
                                  }}
                                >
                                  {t.versionPicker.prereleaseBadge}
                                </Tag>
                              )}
                            </div>
                          </Flex>
                          {timeStr && (
                            <Typography.Text
                              type="secondary"
                              style={{
                                fontSize: 11,
                                lineHeight: '20px',
                                fontFamily: token.fontFamilyCode,
                                flexShrink: 0,
                              }}
                            >
                              {timeStr}
                            </Typography.Text>
                          )}
                        </Flex>
                      )
                    })}

                    {trimmedQuery && !exactTagExists && (
                      <Flex
                        role="option"
                        aria-selected={selectedTag === trimmedQuery}
                        tabIndex={0}
                        align="center"
                        justify="space-between"
                        gap={8}
                        onClick={() => applyTagSelection(trimmedQuery)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault()
                            applyTagSelection(trimmedQuery)
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
              </div>
            </Col>

            {/* 右栏：更新完成后的提醒设置 */}
            <Col
              xs={24}
              md={10}
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
                {candidateTracks.length >= 2 && (
                  <div
                    style={{
                      paddingBottom: 12,
                      marginBottom: 4,
                      borderBottom: `1px solid ${token.colorSplit}`,
                    }}
                  >
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 8 }}
                    >
                      {t.versionPicker.candidatesHeader}
                    </Typography.Text>
                    <Flex wrap="wrap" gap={6}>
                      {candidateTracks.map((cand) => {
                        const active = selectedTag === cand.tag
                        return (
                          <Tooltip key={cand.kind} title={cand.tip}>
                            <Button
                              size="small"
                              type="default"
                              onClick={() => handlePickCandidateTrack(cand.tag)}
                              style={{
                                height: 'auto',
                                padding: '3px 8px',
                                display: 'inline-flex',
                                alignItems: 'center',
                                gap: 6,
                                borderColor: active
                                  ? token.colorPrimary
                                  : token.colorBorderSecondary,
                                background: active ? token.colorPrimaryBg : token.colorBgContainer,
                              }}
                            >
                              <Tag
                                color={cand.color}
                                style={{
                                  margin: 0,
                                  fontSize: 11,
                                  lineHeight: '16px',
                                  paddingInline: 5,
                                }}
                              >
                                {cand.label}
                              </Tag>
                              <span
                                style={{
                                  fontFamily: token.fontFamilyCode,
                                  fontSize: token.fontSizeSM,
                                  fontWeight: active ? 600 : 400,
                                  color: active ? token.colorPrimary : token.colorText,
                                }}
                              >
                                {cand.tag}
                              </span>
                            </Button>
                          </Tooltip>
                        )
                      })}
                    </Flex>
                  </div>
                )}

                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  {t.versionPicker.policyHeader}
                </Typography.Text>

                {/* 1. 正常提醒所有新版本 */}
                {renderRadioCard(
                  'all',
                  t.versionPicker.policyAllTitle,
                  isSelectedPrerelease
                    ? t.versionPicker.policyAllPrereleaseDesc
                    : t.versionPicker.policyAllDesc,
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
        /* 右页签：按配置更新（支持单容器 YAML 与独立容器 docker run 双向可视化编辑） */
        <Flex vertical gap={12}>
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
            /* Case A：单容器 YAML 项目（上方 compose.yaml 与下方表单实时双向同步） */
            <>
              {app && needYamlSaveChoice && (
                <ComposeSaveLocationBar
                  appKey={app.key}
                  hostName={yamlHostName || app.hostName}
                  projectName={yamlProjectName || app.projectName || app.name}
                  composePath={yamlComposePath}
                  pathMissing={yamlPathMissing}
                  pathReadOnly={yamlPathReadOnly}
                  origWritable={yamlOrigWritable}
                  suggestedDirs={yamlSuggestedDirs}
                  saveMode={yamlSaveMode}
                  pickedNewFile={yamlPickedNewFile}
                  onChangeSaveMode={setYamlSaveMode}
                  onSelectNewFile={setYamlPickedNewFile}
                />
              )}

              <ConfigVisualEditor
                mode="yaml"
                textValue={yamlText}
                origTextValue={origYaml}
                origConfigs={origYamlConfigs}
                curConfigs={curYamlConfigs}
                onChangeText={(nextText) => setYamlText(nextText)}
                onChangeService={(svcName, nextSvc) => {
                  setYamlText((prev) => patchComposeServiceInYaml(prev, svcName, nextSvc))
                }}
                onRestoreAllChangedValues={(restoredServices) => {
                  setYamlText((prev) =>
                    restoredServices.reduce(
                      (acc, s) => patchComposeServiceInYaml(acc, s.name, s),
                      prev,
                    ),
                  )
                }}
                onResetAll={() => setYamlText(origYaml)}
              />

              {overwriteRisks.missingServices.length > 0 && (
                <Alert
                  type="warning"
                  showIcon
                  title={
                    <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                      <strong>{t.projectUpdate.warnRemovedPrefix}</strong>
                      {t.projectUpdate.warnRemovedBefore}
                      <Space size={4} wrap style={{ display: 'inline-flex' }}>
                        {overwriteRisks.missingServices.map((name) => (
                          <Typography.Text key={name} code strong>
                            {name}
                          </Typography.Text>
                        ))}
                      </Space>
                      {overwriteRisks.missingServices.length === 1
                        ? t.projectUpdate.warnRemovedSingleAfter
                        : t.projectUpdate.warnRemovedMultiAfter.replace(
                            '{count}',
                            String(overwriteRisks.missingServices.length),
                          )}
                      {overwriteRisks.addedServices.length > 0 && (
                        <>
                          {t.projectUpdate.warnRemovedAddedBefore}
                          <Space size={4} wrap style={{ display: 'inline-flex' }}>
                            {overwriteRisks.addedServices.map((name) => (
                              <Typography.Text key={name} code>
                                {name}
                              </Typography.Text>
                            ))}
                          </Space>
                          {t.projectUpdate.warnRemovedAddedAfter}
                        </>
                      )}
                      {t.projectUpdate.warnRemovedEnd}
                    </Typography.Text>
                  }
                />
              )}
            </>
          ) : (
            /* Case B：独立容器命令模式（上方 docker run 与下方表单实时双向同步） */
            origCmdCfg &&
            cmdCfg && (
              <ConfigVisualEditor
                mode="cmd"
                textValue={cmdText}
                origTextValue={origCmdText}
                origConfigs={[origCmdCfg]}
                curConfigs={[cmdCfg]}
                onChangeText={(nextText) => {
                  setCmdText(nextText)
                  setCmdCfg(parseDockerRunCommand(nextText, origCmdCfg.name))
                }}
                onChangeService={(_svcName, nextSvc) => {
                  setCmdCfg(nextSvc)
                  setCmdText(buildDockerRunCommand(nextSvc))
                }}
                onRestoreAllChangedValues={(restoredServices) => {
                  const first = restoredServices[0]
                  if (!first) return
                  setCmdCfg(first)
                  setCmdText(buildDockerRunCommand(first))
                }}
                onResetAll={() => {
                  setCmdCfg(cloneEditableConfig(origCmdCfg))
                  setCmdText(origCmdText)
                }}
              />
            )
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
            <Typography.Text
              type={needYamlSaveChoice && !effectiveYamlTargetFile ? 'warning' : 'secondary'}
              style={{ fontSize: token.fontSizeSM }}
            >
              {needYamlSaveChoice
                ? effectiveYamlTargetFile
                  ? t.composeSaveLocation.willSaveToHint.replace('{path}', effectiveYamlTargetFile)
                  : t.composeSaveLocation.mustPickHint
                : t.projectUpdate.autoRollbackNote}
            </Typography.Text>
            <Space size={8}>
              <Button onClick={onClose}>{t.versionPicker.cancel}</Button>
              <Button
                type="primary"
                disabled={
                  !configEditable ||
                  loadingConfig ||
                  (!isStandaloneConfigMode && !yamlText.trim()) ||
                  (needYamlSaveChoice && !effectiveYamlTargetFile) ||
                  (isStandaloneConfigMode && !cmdText.trim())
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

      {app && (
        <DeleteAppModal
          open={Boolean(pendingRemovalConfirm)}
          appKey={app.key}
          appName={app.projectName || app.appName || app.name}
          hostName={app.hostName}
          source={app.source}
          composeRemoval={
            pendingRemovalConfirm
              ? {
                  removedServices: pendingRemovalConfirm.removedServices,
                  addedServices: pendingRemovalConfirm.addedServices,
                  newContent: yamlText,
                  onConfirm: async ({ deleteImages, fileIds }) => {
                    await executeYamlConfigOrPrompt({
                      confirmRemoveServices: true,
                      deleteRemovedImages: deleteImages,
                      deleteRemovedFileIds: fileIds,
                    })
                  },
                }
              : undefined
          }
          onClose={() => setPendingRemovalConfirm(null)}
        />
      )}

      <StoppedUpdateModal
        open={Boolean(stoppedPending)}
        title={t.lifecycle.stoppedSingleTitle}
        description={t.lifecycle.stoppedSingleDesc.replace(
          '{name}',
          service.name && service.name !== 'main' ? service.name : appName,
        )}
        loadingChoice={stoppedChoiceLoading}
        onConfirm={async (startAfter) => {
          if (!stoppedPending) return
          if (stoppedPending.kind === 'version') {
            await doSubmitVersion(startAfter)
            return
          }
          if (stoppedPending.kind === 'standalone') {
            await doStandaloneSubmit(stoppedPending.payload, startAfter)
            return
          }
          setStoppedChoiceLoading(startAfter ? 'start' : 'keep')
          setSubmittingConfig(true)
          try {
            await submitYamlConfigWithOptions({
              ...stoppedPending.opts,
              startAfter,
            })
          } catch (e) {
            if (
              e instanceof ApiError &&
              e.code === 'compose_services_removed' &&
              e.removedServices?.length
            ) {
              setStoppedPending(null)
              setPendingRemovalConfirm({
                removedServices: e.removedServices,
                addedServices: e.addedServices ?? [],
              })
              return
            }
            showApiError(e)
          } finally {
            setStoppedChoiceLoading(null)
            setSubmittingConfig(false)
          }
        }}
        onCancel={() => setStoppedPending(null)}
      />
    </Flex>
  )
}
