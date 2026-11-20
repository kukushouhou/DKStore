import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AutoComplete,
  Button,
  Checkbox,
  Flex,
  Grid,
  Input,
  Modal,
  Popover,
  Space,
  Spin,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  ArrowUpOutlined,
  CloseOutlined,
  DownOutlined,
  ExclamationCircleFilled,
  PlusOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  showApiError,
  type HostItem,
  type HostMirrorContainerItem,
  type MirrorPair,
  type MirrorTestItem,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'

interface UrlRowState {
  key: string
  url: string
}

interface PairRowState {
  key: string
  source: string
  selectedTarget: string
  testing: boolean
  urls: UrlRowState[]
  testMap: Record<string, MirrorTestItem>
}

let rowSeq = 1
function nextKey(prefix: string): string {
  rowSeq += 1
  return `${prefix}_${rowSeq}`
}

function normalizeSourceDomain(raw: string): string {
  const s = raw
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//i, '')
    .replace(/\/+.*$/, '')
    .trim()
  if (
    s === 'index.docker.io' ||
    s === 'registry-1.docker.io' ||
    s === 'registry.hub.docker.com'
  ) {
    return 'docker.io'
  }
  return s
}

function normalizeMirrorUrl(raw: string): string {
  const s = raw.trim().replace(/\/+$/, '')
  if (!s) return ''
  if (/^https?:\/\//i.test(s)) {
    return s
  }
  return `https://${s}`
}

function normalizeCustomTargetImage(raw: string): string {
  const s = raw.trim().replace(/^\/+|\/+$/g, '')
  if (!s) return ''
  const lastSlash = s.lastIndexOf('/')
  const lastColon = s.lastIndexOf(':')
  if (lastColon > lastSlash) {
    return s
  }
  return `${s}:latest`
}

function sameStringArray(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false
  }
  return true
}

export default function HostMirrorModal() {
  const hostId = useUIStore((s) => s.activeMirrorHostId)
  const setActiveMirrorHostId = useUIStore((s) => s.setActiveMirrorHostId)
  const screens = Grid.useBreakpoint()

  return (
    <Modal
      open={Boolean(hostId)}
      onCancel={() => setActiveMirrorHostId(null)}
      footer={null}
      width={screens.md ? 720 : '96vw'}
      destroyOnHidden
      title={null}
      closable={false}
      styles={{ body: { paddingTop: 4 } }}
    >
      {hostId && (
        <HostMirrorModalBody
          key={hostId}
          hostId={hostId}
          onClose={() => setActiveMirrorHostId(null)}
        />
      )}
    </Modal>
  )
}

function HostMirrorModalBody({
  hostId,
  onClose,
}: {
  hostId: string
  onClose: () => void
}) {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const screens = Grid.useBreakpoint()
  const qc = useQueryClient()
  const [modal, modalContextHolder] = Modal.useModal()

  const hostsQuery = useQuery({
    queryKey: ['hosts', locale],
    queryFn: () => api.listHosts(),
  })
  const allHosts: HostItem[] = useMemo(() => hostsQuery.data?.items ?? [], [hostsQuery.data])
  const currentHost = useMemo(
    () => allHosts.find((h) => h.id === hostId) ?? null,
    [allHosts, hostId],
  )
  const peerHosts = useMemo(
    () => allHosts.filter((h) => h.id !== hostId),
    [allHosts, hostId],
  )

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [rows, setRows] = useState<PairRowState[]>([])
  const [localContainers, setLocalContainers] = useState<HostMirrorContainerItem[]>([])
  const [initialDockerHubUrls, setInitialDockerHubUrls] = useState<string[]>([])
  const [alsoApply, setAlsoApply] = useState(false)
  const [selectedPeerIds, setSelectedPeerIds] = useState<string[]>([])

  // 右上角「▾」弹出菜单状态与新卡片自动聚焦展开状态
  const [openMenuKey, setOpenMenuKey] = useState<string | null>(null)
  const [autoFocusPairKey, setAutoFocusPairKey] = useState<string | null>(null)
  const [searchKeyword, setSearchKeyword] = useState('')
  const [remoteSearchItems, setRemoteSearchItems] = useState<string[]>([])
  const [searchingRemote, setSearchingRemote] = useState(false)
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const searchSeqRef = useRef(0)

  useEffect(() => {
    return () => {
      if (searchTimerRef.current) {
        clearTimeout(searchTimerRef.current)
      }
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    api
      .getHostMirrors(hostId)
      .then((res) => {
        if (cancelled) return
        const pairs = res.pairs?.length
          ? res.pairs
          : [{ source: 'docker.io', urls: [] }]
        const hubPair = pairs.find((p) => normalizeSourceDomain(p.source) === 'docker.io')
        setInitialDockerHubUrls(
          (hubPair?.urls ?? []).map(normalizeMirrorUrl).filter(Boolean),
        )
        setLocalContainers(res.localContainers ?? [])
        setRows(
          pairs.map((p) => ({
            key: nextKey('pair'),
            source: p.source || 'docker.io',
            selectedTarget: '',
            testing: false,
            testMap: {},
            urls:
              p.urls && p.urls.length > 0
                ? p.urls.map((u) => ({ key: nextKey('url'), url: u }))
                : [{ key: nextKey('url'), url: '' }],
          })),
        )
      })
      .catch((e) => {
        if (!cancelled) {
          showApiError(e)
          setRows([
            {
              key: nextKey('pair'),
              source: 'docker.io',
              selectedTarget: '',
              testing: false,
              testMap: {},
              urls: [{ key: nextKey('url'), url: '' }],
            },
          ])
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [hostId])

  const presetSources = useMemo(
    () => [
      { domain: 'docker.io', desc: t.hosts.presetDockerHub },
      { domain: 'ghcr.io', desc: t.hosts.presetGhcr },
      { domain: 'lscr.io', desc: t.hosts.presetLscr },
      { domain: 'quay.io', desc: t.hosts.presetQuay },
      { domain: 'gcr.io', desc: t.hosts.presetGcr },
      { domain: 'registry.k8s.io', desc: t.hosts.presetK8s },
      { domain: 'mcr.microsoft.com', desc: t.hosts.presetMcr },
    ],
    [t.hosts],
  )

  const buildSourceOptionsForRow = (pairIdx: number, currentVal: string) => {
    const usedOthers = new Set<string>()
    rows.forEach((r, idx) => {
      if (idx !== pairIdx) {
        const norm = normalizeSourceDomain(r.source)
        if (norm) usedOthers.add(norm)
      }
    })
    const q = currentVal.trim().toLowerCase()
    return presetSources
      .filter(
        (p) =>
          !usedOthers.has(p.domain) &&
          (!q || p.domain.includes(q) || p.desc.toLowerCase().includes(q)),
      )
      .map((p) => ({
        value: p.domain,
        label: (
          <Flex align="center" justify="space-between" gap={12}>
            <span style={{ fontFamily: token.fontFamilyCode, fontWeight: 500 }}>
              {p.domain}
            </span>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {p.desc}
            </Typography.Text>
          </Flex>
        ),
      }))
  }

  const buildCleanedPairs = (): MirrorPair[] => {
    const out: MirrorPair[] = []
    const bySource = new Map<string, string[]>()
    const order: string[] = []
    for (const r of rows) {
      const src = normalizeSourceDomain(r.source)
      if (!src) continue
      if (!bySource.has(src)) {
        bySource.set(src, [])
        order.push(src)
      }
      const list = bySource.get(src)!
      for (const u of r.urls) {
        const cleaned = normalizeMirrorUrl(u.url)
        if (cleaned && !list.includes(cleaned)) {
          list.push(cleaned)
        }
      }
    }
    if (!bySource.has('docker.io')) {
      order.unshift('docker.io')
      bySource.set('docker.io', [])
    }
    for (const src of order) {
      const urls = bySource.get(src) ?? []
      if (urls.length === 0 && src !== 'docker.io') continue
      out.push({ source: src, urls })
    }
    return out
  }

  const handleTestPair = async (pairIdx: number, overrideTarget?: string) => {
    const pair = rows[pairIdx]
    if (!pair) return
    const normSrc = normalizeSourceDomain(pair.source) || 'docker.io'
    const pairUrls: string[] = []
    for (const u of pair.urls) {
      const cleaned = normalizeMirrorUrl(u.url)
      if (cleaned && !pairUrls.includes(cleaned)) {
        pairUrls.push(cleaned)
      }
    }
    if (pairUrls.length === 0) {
      message.warning(t.hosts.mirrorTestEmptyMsg)
      return
    }

    const effectiveTarget =
      overrideTarget !== undefined ? overrideTarget : pair.selectedTarget
    const targetKey = pair.key

    setRows((prev) =>
      prev.map((item) =>
        item.key === targetKey
          ? {
              ...item,
              source: normSrc,
              selectedTarget: effectiveTarget,
              testing: true,
              urls: item.urls.map((u) => ({
                ...u,
                url: u.url.trim() ? normalizeMirrorUrl(u.url) : '',
              })),
            }
          : item,
      ),
    )

    try {
      const res = await api.testHostMirrors(hostId, normSrc, pairUrls, effectiveTarget)
      const nextMap: Record<string, MirrorTestItem> = {}
      for (const it of res.items ?? []) {
        nextMap[normalizeMirrorUrl(it.url)] = it
      }
      setRows((prev) =>
        prev.map((item) =>
          item.key === targetKey
            ? { ...item, testing: false, testMap: nextMap }
            : item,
        ),
      )
      message.success(t.hosts.mirrorTestDoneMsg)
    } catch (e) {
      setRows((prev) =>
        prev.map((item) =>
          item.key === targetKey ? { ...item, testing: false } : item,
        ),
      )
      showApiError(e)
    }
  }

  const handleMenuOpenChange = (pairKey: string, nextOpen: boolean) => {
    if (searchTimerRef.current) {
      clearTimeout(searchTimerRef.current)
      searchTimerRef.current = null
    }
    searchSeqRef.current += 1
    setSearchingRemote(false)
    setRemoteSearchItems([])
    setSearchKeyword('')
    setOpenMenuKey(nextOpen ? pairKey : null)
  }

  const handleSearchInputChange = (val: string, rawSource: string) => {
    setSearchKeyword(val)
    if (searchTimerRef.current) {
      clearTimeout(searchTimerRef.current)
      searchTimerRef.current = null
    }
    const q = val.trim()
    const normSrc = normalizeSourceDomain(rawSource) || 'docker.io'
    if (q.length < 2 || (normSrc !== 'docker.io' && normSrc !== 'quay.io')) {
      setSearchingRemote(false)
      setRemoteSearchItems([])
      return
    }
    setSearchingRemote(true)
    const seq = ++searchSeqRef.current
    searchTimerRef.current = setTimeout(() => {
      api
        .searchHostMirrorRepos(hostId, normSrc, q)
        .then((res) => {
          if (searchSeqRef.current !== seq) return
          setRemoteSearchItems(res.items ?? [])
        })
        .catch(() => {
          if (searchSeqRef.current !== seq) return
          setRemoteSearchItems([])
        })
        .finally(() => {
          if (searchSeqRef.current === seq) {
            setSearchingRemote(false)
          }
        })
    }, 260)
  }

  const pickTargetAndTest = (pairIdx: number, targetImg: string) => {
    setOpenMenuKey(null)
    setSearchKeyword('')
    setRemoteSearchItems([])
    void handleTestPair(pairIdx, targetImg)
  }

  const executeSave = async (pairs: MirrorPair[], applyPeerIds: string[]) => {
    setSaving(true)
    try {
      await api.saveHostMirrors(hostId, pairs, applyPeerIds)
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['hosts'] }),
        qc.invalidateQueries({ queryKey: ['updates'] }),
      ])
      message.success(
        applyPeerIds.length > 0
          ? t.hosts.mirrorSavedWithSyncMsg
          : t.hosts.mirrorSavedMsg,
      )
      onClose()
    } catch (e) {
      showApiError(e)
    } finally {
      setSaving(false)
    }
  }

  const handleSaveClick = () => {
    const pairs = buildCleanedPairs()
    const applyPeerIds = alsoApply ? selectedPeerIds : []
    const nextDockerHubUrls =
      pairs.find((p) => p.source === 'docker.io')?.urls ?? []

    const affectedSynoNames: string[] = []
    if (
      currentHost?.transport === 'dsm_api' &&
      !sameStringArray(initialDockerHubUrls, nextDockerHubUrls)
    ) {
      affectedSynoNames.push(currentHost.name)
    }
    for (const pid of applyPeerIds) {
      const ph = peerHosts.find((h) => h.id === pid)
      if (ph?.transport === 'dsm_api') {
        affectedSynoNames.push(ph.name)
      }
    }

    if (affectedSynoNames.length > 0) {
      modal.confirm({
        title: t.hosts.mirrorSynoConfirmTitle,
        content: t.hosts.mirrorSynoConfirmDesc.replace(
          '{names}',
          affectedSynoNames.join('、'),
        ),
        okText: t.hosts.mirrorSynoConfirmOk,
        cancelText: t.ignoreModal.cancel,
        onOk: () => executeSave(pairs, applyPeerIds),
      })
      return
    }
    void executeSave(pairs, applyPeerIds)
  }

  const updateRowSource = (pairIdx: number, val: string) => {
    setRows((prev) =>
      prev.map((item, idx) =>
        idx === pairIdx
          ? { ...item, source: val, selectedTarget: '', testMap: {} }
          : item,
      ),
    )
  }

  const normalizeRowSourceOnBlur = (pairIdx: number) => {
    setRows((prev) =>
      prev.map((item, idx) =>
        idx === pairIdx
          ? { ...item, source: normalizeSourceDomain(item.source) }
          : item,
      ),
    )
  }

  const updateUrlText = (pairIdx: number, urlIdx: number, val: string) => {
    setRows((prev) =>
      prev.map((item, idx) => {
        if (idx !== pairIdx) return item
        const nextUrls = item.urls.map((u, uIdx) =>
          uIdx === urlIdx ? { ...u, url: val } : u,
        )
        return { ...item, urls: nextUrls }
      }),
    )
  }

  const normalizeUrlOnBlur = (pairIdx: number, urlIdx: number) => {
    setRows((prev) =>
      prev.map((item, idx) => {
        if (idx !== pairIdx) return item
        const nextUrls = item.urls.map((u, uIdx) =>
          uIdx === urlIdx
            ? { ...u, url: u.url.trim() ? normalizeMirrorUrl(u.url) : '' }
            : u,
        )
        return { ...item, urls: nextUrls }
      }),
    )
  }

  const handlePasteUrls = (
    e: React.ClipboardEvent<HTMLInputElement>,
    pairIdx: number,
    urlIdx: number,
  ) => {
    const raw = e.clipboardData?.getData('text') || ''
    const parts = raw
      .split(/[\r\n,\s]+/)
      .map(normalizeMirrorUrl)
      .filter(Boolean)
    if (parts.length <= 1) return
    e.preventDefault()
    setRows((prev) =>
      prev.map((item, idx) => {
        if (idx !== pairIdx) return item
        const nextUrls = [...item.urls]
        nextUrls.splice(
          urlIdx,
          1,
          ...parts.map((u) => ({ key: nextKey('url'), url: u })),
        )
        return { ...item, urls: nextUrls }
      }),
    )
    message.success(t.hosts.mirrorSplitPasteMsg.replace('{n}', String(parts.length)))
  }

  const moveUrlUp = (pairIdx: number, urlIdx: number) => {
    if (urlIdx <= 0) return
    setRows((prev) =>
      prev.map((item, idx) => {
        if (idx !== pairIdx) return item
        const nextUrls = [...item.urls]
        const tmp = nextUrls[urlIdx - 1]
        nextUrls[urlIdx - 1] = nextUrls[urlIdx]
        nextUrls[urlIdx] = tmp
        return { ...item, urls: nextUrls }
      }),
    )
  }

  const removeUrl = (pairIdx: number, urlIdx: number) => {
    setRows((prev) =>
      prev.map((item, idx) => {
        if (idx !== pairIdx) return item
        const nextUrls = item.urls.filter((_, i) => i !== urlIdx)
        if (nextUrls.length === 0) {
          nextUrls.push({ key: nextKey('url'), url: '' })
        }
        return { ...item, urls: nextUrls }
      }),
    )
  }

  const addBlankUrl = (pairIdx: number) => {
    setRows((prev) =>
      prev.map((item, idx) =>
        idx === pairIdx
          ? { ...item, urls: [...item.urls, { key: nextKey('url'), url: '' }] }
          : item,
      ),
    )
  }

  const addBlankPair = () => {
    const newKey = nextKey('pair')
    setAutoFocusPairKey(newKey)
    setRows((prev) => [
      ...prev,
      {
        key: newKey,
        source: '',
        selectedTarget: '',
        testing: false,
        testMap: {},
        urls: [{ key: nextKey('url'), url: '' }],
      },
    ])
  }

  const removePair = (pairIdx: number) => {
    setRows((prev) => prev.filter((_, idx) => idx !== pairIdx))
  }

  const renderTargetPopover = (pair: PairRowState, pairIdx: number) => {
    const normSrc = normalizeSourceDomain(pair.source) || 'docker.io'
    const pairLocals = localContainers.filter((c) => c.source === normSrc)
    const q = searchKeyword.trim().toLowerCase()
    const isAuto = !pair.selectedTarget

    let defaultTagText = t.hosts.mirrorTagPingOnly
    let defaultTagIsWarn = true
    if (pairLocals.length > 0) {
      defaultTagText = t.hosts.mirrorTagSample.replace('{name}', pairLocals[0].name)
      defaultTagIsWarn = false
    } else if (normSrc === 'docker.io') {
      defaultTagText = t.hosts.mirrorTagBasicImage
      defaultTagIsWarn = true
    }

    const matchedLocals = q
      ? pairLocals.filter(
          (c) =>
            c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q),
        )
      : pairLocals

    const customCandidate = q ? normalizeCustomTargetImage(searchKeyword) : ''
    const hasExactRemote = remoteSearchItems.some(
      (r) => `${r}:latest`.toLowerCase() === customCandidate.toLowerCase(),
    )

    const optionRowStyle = (active: boolean): React.CSSProperties => ({
      width: '100%',
      textAlign: 'left',
      border: 'none',
      borderRadius: token.borderRadius,
      padding: '7px 10px',
      cursor: 'pointer',
      background: active ? token.controlItemBgActive : 'transparent',
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: 8,
    })

    return (
      <Flex vertical gap={8} style={{ width: '100%' }}>
        {/* 顶部淡橙色微提示卡：明确说明第三方镜像源可能未全量同步 */}
        <Flex
          align="flex-start"
          gap={8}
          style={{
            background: token.colorWarningBg,
            border: `1px solid ${token.colorWarningBorder}`,
            borderRadius: token.borderRadius,
            padding: '8px 10px',
          }}
        >
          <ExclamationCircleFilled
            style={{ color: token.colorWarning, marginTop: 3, flexShrink: 0 }}
          />
          <div style={{ lineHeight: 1.45, fontSize: 12 }}>
            <div style={{ fontWeight: 600, color: token.colorText }}>
              {t.hosts.mirrorNoticeTitle}
            </div>
            <div style={{ color: token.colorTextSecondary, marginTop: 2 }}>
              {t.hosts.mirrorNoticeDesc}
            </div>
          </div>
        </Flex>

        <Input
          size="small"
          allowClear
          value={searchKeyword}
          onChange={(e) => handleSearchInputChange(e.target.value, pair.source)}
          onPressEnter={() => {
            if (customCandidate) {
              pickTargetAndTest(pairIdx, customCandidate)
            }
          }}
          placeholder={t.hosts.mirrorSearchPlaceholder}
        />

        <div style={{ maxHeight: 205, overflowY: 'auto' }}>
          <Flex vertical gap={2}>
            {!q ? (
              <>
                <button
                  type="button"
                  style={optionRowStyle(isAuto)}
                  onClick={() => pickTargetAndTest(pairIdx, '')}
                >
                  <Typography.Text strong style={{ fontSize: 12, flexShrink: 0 }}>
                    {isAuto ? '✓ ' : ''}
                    {t.hosts.mirrorDefaultTestLabel}
                  </Typography.Text>
                  <span
                    style={{
                      fontSize: 11,
                      padding: '1px 6px',
                      borderRadius: 4,
                      maxWidth: 185,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                      fontFamily: defaultTagIsWarn ? undefined : token.fontFamilyCode,
                      background: defaultTagIsWarn
                        ? token.colorWarningBg
                        : token.colorFillSecondary,
                      color: defaultTagIsWarn
                        ? token.colorWarningText
                        : token.colorTextSecondary,
                    }}
                  >
                    {defaultTagText}
                  </span>
                </button>

                {pairLocals.length > 0 && (
                  <>
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: 11, padding: '6px 10px 2px' }}
                    >
                      {t.hosts.mirrorLocalGroup}
                    </Typography.Text>
                    {pairLocals.map((c) => {
                      const active = pair.selectedTarget === c.image
                      return (
                        <button
                          key={`${c.source}|${c.image}`}
                          type="button"
                          style={optionRowStyle(active)}
                          onClick={() => pickTargetAndTest(pairIdx, c.image)}
                        >
                          <Typography.Text
                            strong
                            style={{ fontSize: 12, flexShrink: 0 }}
                          >
                            {active ? '✓ ' : ''}
                            {c.name}
                          </Typography.Text>
                          <Typography.Text
                            type="secondary"
                            ellipsis
                            style={{
                              fontSize: 12,
                              fontFamily: token.fontFamilyCode,
                            }}
                          >
                            {c.image}
                          </Typography.Text>
                        </button>
                      )
                    })}
                  </>
                )}
              </>
            ) : (
              <>
                {matchedLocals.map((c) => (
                  <button
                    key={`m_${c.source}|${c.image}`}
                    type="button"
                    style={optionRowStyle(pair.selectedTarget === c.image)}
                    onClick={() => pickTargetAndTest(pairIdx, c.image)}
                  >
                    <Typography.Text strong style={{ fontSize: 12, flexShrink: 0 }}>
                      {c.name}
                    </Typography.Text>
                    <Typography.Text
                      type="secondary"
                      ellipsis
                      style={{ fontSize: 12, fontFamily: token.fontFamilyCode }}
                    >
                      {c.image}
                    </Typography.Text>
                  </button>
                ))}

                {searchingRemote && (
                  <div style={{ textAlign: 'center', padding: '6px 0' }}>
                    <Spin size="small" />
                  </div>
                )}

                {remoteSearchItems.map((repo) => {
                  const full = `${repo}:latest`
                  return (
                    <button
                      key={`r_${repo}`}
                      type="button"
                      style={optionRowStyle(pair.selectedTarget === full)}
                      onClick={() => pickTargetAndTest(pairIdx, full)}
                    >
                      <Typography.Text
                        ellipsis
                        style={{ fontSize: 12, fontFamily: token.fontFamilyCode }}
                      >
                        {full}
                      </Typography.Text>
                    </button>
                  )
                })}

                {customCandidate && !hasExactRemote && (
                  <button
                    type="button"
                    style={optionRowStyle(false)}
                    onClick={() => pickTargetAndTest(pairIdx, customCandidate)}
                  >
                    <Typography.Text
                      ellipsis
                      style={{
                        fontSize: 12,
                        color: token.colorLink,
                        fontFamily: token.fontFamilyCode,
                      }}
                    >
                      {t.hosts.mirrorVerifyCustom.replace('{image}', customCandidate)}
                    </Typography.Text>
                  </button>
                )}
              </>
            )}
          </Flex>
        </div>
      </Flex>
    )
  }

  const renderUrlResultPill = (pair: PairRowState, rawUrl: string) => {
    const normUrl = normalizeMirrorUrl(rawUrl)
    const tested = normUrl ? pair.testMap[normUrl] : undefined
    if (!tested) return null

    const status = tested.status || (tested.ok ? 'ok' : 'dead')
    const normSrc = normalizeSourceDomain(pair.source) || 'docker.io'
    const matchedLocal = !pair.selectedTarget
      ? localContainers.find(
          (c) => c.source === normSrc && c.image === tested.testedImage,
        )
      : undefined
    const friendlyTarget = matchedLocal
      ? t.hosts.mirrorTargetLocalPrefix.replace('{name}', matchedLocal.name)
      : (tested.testedImage || normSrc).replace(/:latest$/i, '')

    let bg = token.colorSuccessBg
    let fg = token.colorSuccessText
    let label = `● ${tested.latencyMs ?? 1} ms`
    let tipTitle = t.hosts.mirrorTipOkTitle
    let tipBody = t.hosts.mirrorTipOkBody.replace('{target}', friendlyTarget)

    if (status === 'basic_ok') {
      bg = token.colorSuccessBg
      fg = token.colorSuccessText
      label = `● ${tested.latencyMs ?? 1} ms · ${t.hosts.mirrorStateBasicSuffix}`
      tipTitle = t.hosts.mirrorTipBasicTitle
      tipBody = t.hosts.mirrorTipBasicBody
    } else if (status === 'restricted') {
      bg = token.colorErrorBg
      fg = token.colorErrorText
      label = `✖ ${t.hosts.mirrorStateRestricted}`
      tipTitle = t.hosts.mirrorTipRestrictedTitle
      tipBody = t.hosts.mirrorTipRestrictedBody.replace('{target}', friendlyTarget)
    } else if (status === 'not_found') {
      bg = token.colorErrorBg
      fg = token.colorErrorText
      label = `✖ ${t.hosts.mirrorStateNotFound}`
      tipTitle = t.hosts.mirrorTipNotFoundTitle
      tipBody = t.hosts.mirrorTipNotFoundBody.replace('{target}', friendlyTarget)
    } else if (status === 'dead' || !tested.ok) {
      bg = token.colorErrorBg
      fg = token.colorErrorText
      label = `✖ ${t.hosts.mirrorStateBad}`
      tipTitle = t.hosts.mirrorTipDeadTitle
      tipBody = t.hosts.mirrorTipDeadFallback
    }

    return (
      <Popover
        trigger="hover"
        placement="topRight"
        styles={{ container: { maxWidth: 275, padding: '8px 10px' } }}
        content={
          <div style={{ fontSize: 12, lineHeight: 1.5 }}>
            <div style={{ fontWeight: 600, color: token.colorText }}>{tipTitle}</div>
            <div style={{ color: token.colorTextSecondary, marginTop: 2 }}>
              {tipBody}
            </div>
          </div>
        }
      >
        <span
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            padding: '1px 7px',
            borderRadius: 4,
            background: bg,
            color: fg,
            fontSize: 12,
            fontWeight: 500,
            whiteSpace: 'nowrap',
            cursor: 'help',
          }}
        >
          {label}
        </span>
      </Popover>
    )
  }

  const titleText = t.hosts.mirrorModalTitle.replace(
    '{name}',
    currentHost?.name ?? hostId,
  )

  return (
    <Flex vertical gap={14}>
      {modalContextHolder}

      {/* 弹窗标题 */}
      <div
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <Typography.Title level={5} style={{ margin: 0 }}>
          {titleText}
        </Typography.Title>
      </div>

      {loading ? (
        <div style={{ textAlign: 'center', padding: '32px 0' }}>
          <Spin />
        </div>
      ) : (
        <>
          {/* 配对卡片列表 */}
          <Flex
            vertical
            gap={12}
            style={{ maxHeight: 400, overflowY: 'auto', paddingRight: 2 }}
          >
            {rows.map((pair, pairIdx) => {
              const isMenuOpen = openMenuKey === pair.key
              return (
                <div
                  key={pair.key}
                  style={{
                    background: token.colorFillAlter,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    borderRadius: token.borderRadiusLG,
                    padding: 14,
                  }}
                >
                  {/* 卡片头：左「源仓库」 + 右「⚡ | ▾」分段图标按钮 */}
                  <Flex align="center" justify="space-between" gap={12}>
                    <Flex align="center" gap={10} style={{ minWidth: 0, flex: 1 }}>
                      <Typography.Text
                        type="secondary"
                        style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                      >
                        {t.hosts.mirrorSourceLabel}
                      </Typography.Text>
                      <AutoComplete
                        value={pair.source}
                        autoFocus={pair.key === autoFocusPairKey}
                        defaultOpen={pair.key === autoFocusPairKey}
                        popupMatchSelectWidth={290}
                        options={buildSourceOptionsForRow(pairIdx, pair.source)}
                        onChange={(val) => updateRowSource(pairIdx, val)}
                        onSelect={() => {
                          if (autoFocusPairKey === pair.key) {
                            setAutoFocusPairKey(null)
                          }
                        }}
                        onBlur={() => {
                          if (autoFocusPairKey === pair.key) {
                            setAutoFocusPairKey(null)
                          }
                          normalizeRowSourceOnBlur(pairIdx)
                        }}
                        placeholder={t.hosts.mirrorSourcePlaceholder}
                        style={{
                          width: screens.sm ? 250 : '100%',
                          maxWidth: '100%',
                          fontFamily: token.fontFamilyCode,
                        }}
                      />
                    </Flex>

                    <Flex align="center" gap={8} style={{ flexShrink: 0 }}>
                      <Space.Compact>
                        <Tooltip title={t.hosts.mirrorTestTip}>
                          <Button
                            icon={<ThunderboltOutlined />}
                            loading={pair.testing}
                            disabled={saving}
                            aria-label={t.hosts.mirrorTestTip}
                            onClick={() => void handleTestPair(pairIdx)}
                          />
                        </Tooltip>
                        <Popover
                          open={isMenuOpen}
                          onOpenChange={(open) => handleMenuOpenChange(pair.key, open)}
                          trigger="click"
                          placement="bottomRight"
                          arrow={false}
                          styles={{ container: { padding: 10, width: 365 } }}
                          content={renderTargetPopover(pair, pairIdx)}
                        >
                          <Tooltip title={t.hosts.mirrorPickTargetTip}>
                            <Button
                              icon={<DownOutlined style={{ fontSize: 10 }} />}
                              disabled={pair.testing || saving}
                              aria-label={t.hosts.mirrorPickTargetTip}
                              style={{ position: 'relative', width: 30 }}
                            >
                              {pair.selectedTarget ? (
                                <span
                                  style={{
                                    position: 'absolute',
                                    top: 5,
                                    right: 5,
                                    width: 6,
                                    height: 6,
                                    borderRadius: '50%',
                                    background: token.colorPrimary,
                                  }}
                                />
                              ) : null}
                            </Button>
                          </Tooltip>
                        </Popover>
                      </Space.Compact>

                      {rows.length > 1 && (
                        <Tooltip title={t.hosts.mirrorDelPairBtn}>
                          <Button
                            type="text"
                            icon={<CloseOutlined style={{ fontSize: 12 }} />}
                            onClick={() => removePair(pairIdx)}
                          />
                        </Tooltip>
                      )}
                    </Flex>
                  </Flex>

                  {/* 卡片主体：全宽展示该仓库的加速镜像网址列表 */}
                  <Flex vertical gap={8} style={{ marginTop: 10 }}>
                    {pair.urls.map((uItem, urlIdx) => (
                      <Flex key={uItem.key} align="center" gap={6}>
                        <Input
                          value={uItem.url}
                          onChange={(e) =>
                            updateUrlText(pairIdx, urlIdx, e.target.value)
                          }
                          onBlur={() => normalizeUrlOnBlur(pairIdx, urlIdx)}
                          onPaste={(e) => handlePasteUrls(e, pairIdx, urlIdx)}
                          placeholder={t.hosts.mirrorUrlPlaceholder}
                          style={{
                            flex: 1,
                            minWidth: 0,
                            fontFamily: token.fontFamilyCode,
                          }}
                          suffix={renderUrlResultPill(pair, uItem.url)}
                        />
                        <Tooltip title={t.hosts.mirrorMoveUpTip}>
                          <Button
                            type="text"
                            size="small"
                            icon={<ArrowUpOutlined />}
                            disabled={urlIdx === 0}
                            onClick={() => moveUrlUp(pairIdx, urlIdx)}
                          />
                        </Tooltip>
                        <Button
                          type="text"
                          danger
                          size="small"
                          onClick={() => removeUrl(pairIdx, urlIdx)}
                        >
                          {t.hosts.mirrorDeleteBtn}
                        </Button>
                      </Flex>
                    ))}

                    <div>
                      <Typography.Link
                        style={{ fontSize: token.fontSizeSM }}
                        onClick={() => addBlankUrl(pairIdx)}
                      >
                        {t.hosts.mirrorAddUrlBtn}
                      </Typography.Link>
                    </div>
                  </Flex>
                </div>
              )
            })}
          </Flex>

          {/* 新增配对卡片 */}
          <div>
            <Button
              type="dashed"
              size="small"
              icon={<PlusOutlined />}
              onClick={addBlankPair}
            >
              {t.hosts.mirrorAddPairBtn}
            </Button>
          </div>

          {/* 随保存一并同步应用到其他设备 */}
          {peerHosts.length > 0 && (
            <div
              style={{
                background: token.colorFillAlter,
                borderRadius: token.borderRadius,
                padding: '10px 12px',
              }}
            >
              <Flex vertical gap={8}>
                <Checkbox
                  checked={alsoApply}
                  onChange={(e) => {
                    const checked = e.target.checked
                    setAlsoApply(checked)
                    if (
                      checked &&
                      selectedPeerIds.length === 0 &&
                      peerHosts.length > 0
                    ) {
                      setSelectedPeerIds(peerHosts.map((h) => h.id))
                    }
                  }}
                >
                  <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
                    {t.hosts.mirrorAlsoApplyLabel}
                  </Typography.Text>
                </Checkbox>
                {alsoApply && (
                  <div style={{ paddingLeft: 24 }}>
                    <Checkbox.Group
                      value={selectedPeerIds}
                      onChange={(vals) => setSelectedPeerIds(vals.map(String))}
                      options={peerHosts.map((h) => ({
                        label: h.name,
                        value: h.id,
                      }))}
                    />
                  </div>
                )}
              </Flex>
            </div>
          )}
        </>
      )}

      {/* 标准右对齐底栏 */}
      <Flex
        align="center"
        justify="flex-end"
        gap={8}
        style={{
          paddingTop: 12,
          borderTop: `1px solid ${token.colorSplit}`,
        }}
      >
        <Button onClick={onClose} disabled={saving}>
          {t.ignoreModal.cancel}
        </Button>
        <Button
          type="primary"
          loading={saving}
          disabled={loading}
          onClick={handleSaveClick}
        >
          {t.hosts.saveBtn}
        </Button>
      </Flex>
    </Flex>
  )
}
