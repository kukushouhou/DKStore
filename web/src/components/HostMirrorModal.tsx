import { useEffect, useMemo, useState } from 'react'
import {
  AutoComplete,
  Button,
  Checkbox,
  Col,
  Flex,
  Grid,
  Input,
  Modal,
  Row,
  Spin,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  ArrowUpOutlined,
  CheckCircleFilled,
  CloseCircleFilled,
  PlusOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  showApiError,
  type HostItem,
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
  urls: UrlRowState[]
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
      width={screens.md ? 760 : '96vw'}
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
  const [testing, setTesting] = useState(false)
  const [rows, setRows] = useState<PairRowState[]>([])
  const [initialDockerHubUrls, setInitialDockerHubUrls] = useState<string[]>([])
  const [testMap, setTestMap] = useState<Record<string, MirrorTestItem>>({})
  const [alsoApply, setAlsoApply] = useState(false)
  const [selectedPeerIds, setSelectedPeerIds] = useState<string[]>([])

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
        setRows(
          pairs.map((p) => ({
            key: nextKey('pair'),
            source: p.source || 'docker.io',
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

  const presetSourceOptions = useMemo(() => {
    const presets = [
      { domain: 'docker.io', desc: t.hosts.presetDockerHub },
      { domain: 'ghcr.io', desc: t.hosts.presetGhcr },
      { domain: 'lscr.io', desc: t.hosts.presetLscr },
      { domain: 'quay.io', desc: t.hosts.presetQuay },
      { domain: 'gcr.io', desc: t.hosts.presetGcr },
      { domain: 'registry.k8s.io', desc: t.hosts.presetK8s },
      { domain: 'mcr.microsoft.com', desc: t.hosts.presetMcr },
    ]
    return presets.map((p) => ({
      value: p.domain,
      label: (
        <Flex align="center" justify="space-between" gap={12}>
          <span style={{ fontFamily: token.fontFamilyCode, fontWeight: 500 }}>{p.domain}</span>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {p.desc}
          </Typography.Text>
        </Flex>
      ),
    }))
  }, [t.hosts, token.fontFamilyCode])

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

  const handleTestAll = async () => {
    const allUrls: string[] = []
    for (const r of rows) {
      for (const u of r.urls) {
        const cleaned = normalizeMirrorUrl(u.url)
        if (cleaned && !allUrls.includes(cleaned)) {
          allUrls.push(cleaned)
        }
      }
    }
    if (allUrls.length === 0) {
      message.warning(t.hosts.mirrorTestEmptyMsg)
      return
    }
    // 先将规范化后的 URL 回填展示
    setRows((prev) =>
      prev.map((r) => ({
        ...r,
        source: normalizeSourceDomain(r.source) || r.source,
        urls: r.urls.map((u) => ({
          ...u,
          url: u.url.trim() ? normalizeMirrorUrl(u.url) : '',
        })),
      })),
    )
    setTesting(true)
    try {
      const res = await api.testHostMirrors(hostId, allUrls)
      const nextMap: Record<string, MirrorTestItem> = {}
      for (const item of res.items ?? []) {
        nextMap[normalizeMirrorUrl(item.url)] = item
      }
      setTestMap(nextMap)
      message.success(t.hosts.mirrorTestDoneMsg)
    } catch (e) {
      showApiError(e)
    } finally {
      setTesting(false)
    }
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
      prev.map((item, idx) => (idx === pairIdx ? { ...item, source: val } : item)),
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
    setRows((prev) => [
      ...prev,
      {
        key: nextKey('pair'),
        source: '',
        urls: [{ key: nextKey('url'), url: '' }],
      },
    ])
  }

  const removePair = (pairIdx: number) => {
    setRows((prev) => prev.filter((_, idx) => idx !== pairIdx))
  }

  const titleText = t.hosts.mirrorModalTitle.replace(
    '{name}',
    currentHost?.name ?? hostId,
  )

  return (
    <Flex vertical gap={14}>
      {modalContextHolder}

      {/* 弹窗头部：标题 + 右侧测速检活按钮 */}
      <Flex
        align="center"
        justify="space-between"
        gap={12}
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <Typography.Title level={5} style={{ margin: 0 }}>
          {titleText}
        </Typography.Title>
        <Button
          size="small"
          icon={<ThunderboltOutlined />}
          loading={testing}
          disabled={loading || saving}
          onClick={handleTestAll}
        >
          {testing ? t.hosts.mirrorTestingBtn : t.hosts.mirrorTestAllBtn}
        </Button>
      </Flex>

      {loading ? (
        <div style={{ textAlign: 'center', padding: '32px 0' }}>
          <Spin />
        </div>
      ) : (
        <>
          {/* 列头说明 */}
          <Row gutter={12} style={{ paddingInline: 6 }}>
            <Col xs={24} sm={8}>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {t.hosts.mirrorColSource}
              </Typography.Text>
            </Col>
            <Col xs={24} sm={16}>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {t.hosts.mirrorColUrls}
              </Typography.Text>
            </Col>
          </Row>

          {/* 配对行列表 */}
          <Flex vertical gap={10} style={{ maxHeight: 380, overflowY: 'auto', paddingRight: 2 }}>
            {rows.map((pair, pairIdx) => (
              <div
                key={pair.key}
                style={{
                  background: token.colorFillAlter,
                  border: `1px solid ${token.colorBorderSecondary}`,
                  borderRadius: token.borderRadiusLG,
                  padding: 12,
                }}
              >
                <Row gutter={[12, 10]} align="top">
                  <Col xs={24} sm={8}>
                    <Flex vertical gap={6}>
                      <AutoComplete
                        value={pair.source}
                        options={presetSourceOptions}
                        onChange={(val) => updateRowSource(pairIdx, val)}
                        onBlur={() => normalizeRowSourceOnBlur(pairIdx)}
                        placeholder={t.hosts.mirrorSourcePlaceholder}
                        style={{ width: '100%', fontFamily: token.fontFamilyCode }}
                      />
                      {rows.length > 1 && (
                        <div>
                          <Typography.Link
                            type="danger"
                            style={{ fontSize: 12 }}
                            onClick={() => removePair(pairIdx)}
                          >
                            {t.hosts.mirrorDelPairBtn}
                          </Typography.Link>
                        </div>
                      )}
                    </Flex>
                  </Col>

                  <Col xs={24} sm={16}>
                    <Flex vertical gap={8}>
                      {pair.urls.map((uItem, urlIdx) => {
                        const normUrl = normalizeMirrorUrl(uItem.url)
                        const tested = normUrl ? testMap[normUrl] : undefined
                        return (
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
                              suffix={
                                tested ? (
                                  tested.ok ? (
                                    <span
                                      style={{
                                        color: token.colorSuccess,
                                        fontSize: 12,
                                        fontWeight: 500,
                                        whiteSpace: 'nowrap',
                                      }}
                                    >
                                      <CheckCircleFilled style={{ marginRight: 4 }} />
                                      {tested.latencyMs ?? 1} ms
                                    </span>
                                  ) : (
                                    <Tooltip title={tested.error || t.hosts.mirrorStateBad}>
                                      <span
                                        style={{
                                          color: token.colorError,
                                          fontSize: 12,
                                          fontWeight: 500,
                                          whiteSpace: 'nowrap',
                                        }}
                                      >
                                        <CloseCircleFilled style={{ marginRight: 4 }} />
                                        {t.hosts.mirrorStateBad}
                                      </span>
                                    </Tooltip>
                                  )
                                ) : null
                              }
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
                        )
                      })}

                      <div>
                        <Typography.Link
                          style={{ fontSize: token.fontSizeSM }}
                          onClick={() => addBlankUrl(pairIdx)}
                        >
                          {t.hosts.mirrorAddUrlBtn}
                        </Typography.Link>
                      </div>
                    </Flex>
                  </Col>
                </Row>
              </div>
            ))}
          </Flex>

          {/* 新增配对行 */}
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
                    if (checked && selectedPeerIds.length === 0 && peerHosts.length > 0) {
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
