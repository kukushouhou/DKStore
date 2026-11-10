import { useEffect, useMemo, useState } from 'react'
import { Button, Col, Grid, Modal, Row, Spin, message, theme } from 'antd'
import {
  api,
  type ServiceItem,
  type ServiceTagItem,
  type UpdatePolicyMode,
} from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
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
  initialSelectedTag?: string
  onPickOnly?: (sel: VersionPickSelection) => void
  onClose: () => void
  onUpdated?: () => void
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
          key={`${props.service.key}::${props.initialSelectedTag ?? ''}`}
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
  initialSelectedTag,
  onPickOnly,
  onClose,
  onUpdated,
}: VersionPickerModalProps & { service: ServiceItem }) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const screens = Grid.useBreakpoint()
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)

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

  const handleSubmit = async () => {
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
        className="dkstore-radio-card"
        onClick={() => setPolicy(key)}
        style={{
          padding: '10px 12px',
          borderRadius: 12,
          border: checked
            ? `1px solid ${token.colorPrimary}`
            : `1px solid ${token.colorBorderSecondary}`,
          background: checked ? 'rgba(47, 107, 255, 0.08)' : token.colorFillQuaternary,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
          <input
            type="radio"
            checked={checked}
            onChange={() => setPolicy(key)}
            style={{ marginTop: 3, accentColor: token.colorPrimary, cursor: 'pointer' }}
          />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div
              style={{
                fontSize: 13,
                fontWeight: 500,
                color: checked ? token.colorPrimary : token.colorText,
              }}
            >
              {titleText}
            </div>
            {extraNode}
            {descNode && (
              <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 2 }}>
                {descNode}
              </div>
            )}
          </div>
        </div>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* 顶栏 */}
        <div
          style={{
            display: 'flex',
            flexWrap: 'wrap',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: 12,
            paddingBottom: 14,
            borderBottom: `1px solid ${token.colorBorderSecondary}`,
          }}
        >
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 16, fontWeight: 700, color: token.colorText }}>
              {t.versionPicker.title.replace('{name}', appName)}
            </div>
            <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 2 }}>
              {subtitle} · {t.versionPicker.runningNow}
              <code style={{ fontFamily: 'monospace', color: token.colorText }}>{curVer}</code>
            </div>
          </div>

          <div
            style={{
              padding: '6px 12px',
              borderRadius: 10,
              background: token.colorFillQuaternary,
              border: `1px solid ${token.colorBorderSecondary}`,
              display: 'inline-flex',
              alignItems: 'center',
              gap: 8,
              fontSize: 12,
              maxWidth: '100%',
            }}
          >
            <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
              {t.versionPicker.targetImage}
            </span>
            <code
              style={{
                fontFamily: 'monospace',
                fontWeight: 600,
                color: token.colorPrimary,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {shortRepo}:{selectedTag}
            </code>
          </div>
        </div>

        {/* 双栏主体：桌面左右 10:14，移动端上下叠放 */}
        <Row gutter={[20, 16]} align="stretch">
          <Col xs={24} md={10} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {/* 内嵌式搜索框（输入仅过滤本地列表，回车/点搜索请求远端） */}
            <div style={{ position: 'relative' }}>
              <input
                type="text"
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value)
                  setRemoteSearched(false)
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    handleRemoteSearch()
                  }
                }}
                placeholder={t.versionPicker.searchPlaceholder}
                style={{
                  width: '100%',
                  boxSizing: 'border-box',
                  padding: '8px 74px 8px 12px',
                  borderRadius: 10,
                  border: `1px solid ${token.colorBorder}`,
                  background: token.colorFillQuaternary,
                  color: token.colorText,
                  fontFamily: 'monospace',
                  fontSize: 12,
                  outline: 'none',
                }}
              />
              {trimmedQuery && (
                <button
                  type="button"
                  onClick={() => {
                    setQuery('')
                    setRemoteSearched(false)
                  }}
                  style={{
                    position: 'absolute',
                    right: 54,
                    top: '50%',
                    transform: 'translateY(-50%)',
                    border: 'none',
                    background: 'transparent',
                    color: token.colorTextSecondary,
                    cursor: 'pointer',
                    padding: '2px 4px',
                    fontSize: 12,
                  }}
                >
                  ✕
                </button>
              )}
              <button
                type="button"
                disabled={searchingRemote}
                onClick={handleRemoteSearch}
                style={{
                  position: 'absolute',
                  right: 5,
                  top: '50%',
                  transform: 'translateY(-50%)',
                  padding: '4px 10px',
                  borderRadius: 7,
                  border: 'none',
                  background: 'rgba(47, 107, 255, 0.12)',
                  color: token.colorPrimary,
                  fontSize: 12,
                  fontWeight: 500,
                  cursor: 'pointer',
                }}
              >
                {searchingRemote ? '…' : t.versionPicker.searchBtn}
              </button>
            </div>

            <div style={{ fontSize: 12, color: token.colorTextSecondary, paddingInline: 2 }}>
              {trimmedQuery
                ? t.versionPicker.matchedCaption.replace('{q}', trimmedQuery)
                : t.versionPicker.recentCaption}
            </div>

            {/* 标签列表 */}
            <div
              style={{
                flex: 1,
                maxHeight: 260,
                minHeight: 180,
                overflowY: 'auto',
                borderRadius: 12,
                border: `1px solid ${token.colorBorderSecondary}`,
                background: token.colorFillQuaternary,
              }}
            >
              {loading || searchingRemote ? (
                <div
                  style={{
                    height: 180,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: 8,
                    fontSize: 12,
                    color: token.colorTextSecondary,
                  }}
                >
                  <Spin size="small" />
                  <span>{t.versionPicker.loadingTags}</span>
                </div>
              ) : (
                <>
                  {displayedTags.map((item, idx) => {
                    const active = item.tag === selectedTag
                    const timeStr = formatTagTime(item.updatedAt)
                    return (
                      <button
                        key={item.tag}
                        type="button"
                        className="dkstore-tag-row"
                        onClick={() => applyTagSelection(item.tag, highestTag)}
                        style={{
                          width: '100%',
                          padding: '8px 12px',
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 8,
                          border: 'none',
                          borderBottom:
                            idx < displayedTags.length - 1
                              ? `1px solid ${token.colorBorderSecondary}`
                              : 'none',
                          background: active ? 'rgba(47, 107, 255, 0.10)' : 'transparent',
                          color: active ? token.colorPrimary : token.colorText,
                          fontWeight: active ? 600 : 400,
                          fontSize: 12,
                          textAlign: 'left',
                        }}
                      >
                        <span
                          style={{
                            display: 'inline-flex',
                            alignItems: 'center',
                            gap: 6,
                            fontFamily: 'monospace',
                            minWidth: 0,
                          }}
                        >
                          <span
                            style={{
                              width: 14,
                              display: 'inline-block',
                              color: token.colorPrimary,
                              flexShrink: 0,
                            }}
                          >
                            {active ? '✓' : ''}
                          </span>
                          <span
                            style={{
                              overflow: 'hidden',
                              textOverflow: 'ellipsis',
                              whiteSpace: 'nowrap',
                            }}
                          >
                            {item.tag}
                          </span>
                        </span>
                        {timeStr && (
                          <span
                            style={{
                              fontSize: 11,
                              color: token.colorTextSecondary,
                              fontWeight: 400,
                              flexShrink: 0,
                            }}
                          >
                            {timeStr}
                          </span>
                        )}
                      </button>
                    )
                  })}

                  {trimmedQuery && displayedTags.length === 0 && !remoteSearched && (
                    <button
                      type="button"
                      className="dkstore-tag-row"
                      onClick={handleRemoteSearch}
                      style={{
                        width: '100%',
                        padding: '10px 12px',
                        border: 'none',
                        background: 'transparent',
                        color: token.colorPrimary,
                        fontSize: 12,
                        textAlign: 'left',
                      }}
                    >
                      {t.versionPicker.notFoundInRecent.replace('{q}', trimmedQuery)}
                    </button>
                  )}

                  {trimmedQuery && !exactTagExists && (remoteSearched || localTags.length === 0) && (
                    <button
                      type="button"
                      className="dkstore-tag-row"
                      onClick={() => applyTagSelection(trimmedQuery, highestTag)}
                      style={{
                        width: '100%',
                        padding: '9px 12px',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 8,
                        border: 'none',
                        borderTop:
                          displayedTags.length > 0
                            ? `1px solid ${token.colorBorderSecondary}`
                            : 'none',
                        background:
                          selectedTag === trimmedQuery
                            ? 'rgba(47, 107, 255, 0.10)'
                            : 'transparent',
                        color: token.colorText,
                        fontSize: 12,
                        textAlign: 'left',
                      }}
                    >
                      <span style={{ fontFamily: 'monospace', fontWeight: 600 }}>
                        + {trimmedQuery}
                      </span>
                      <span style={{ color: token.colorPrimary }}>
                        {t.versionPicker.useCustomTag}
                      </span>
                    </button>
                  )}

                  {!trimmedQuery && displayedTags.length === 0 && (
                    <div
                      style={{
                        padding: '24px 12px',
                        textAlign: 'center',
                        fontSize: 12,
                        color: token.colorTextSecondary,
                      }}
                    >
                      {t.versionPicker.emptyTags}
                    </div>
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
              borderLeft: screens.md ? `1px solid ${token.colorBorderSecondary}` : 'none',
              paddingLeft: screens.md ? 20 : 10,
              display: 'flex',
              flexDirection: 'column',
              justifyContent: 'space-between',
              gap: 10,
            }}
          >
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ fontSize: 12, color: token.colorTextSecondary }}>
                {t.versionPicker.policyHeader}
              </div>

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
                <div
                  style={{
                    borderRadius: 10,
                    background: 'rgba(245, 158, 11, 0.10)',
                    border: '1px solid rgba(245, 158, 11, 0.38)',
                    padding: '8px 12px',
                    fontSize: 12,
                    color: '#d97706',
                    lineHeight: 1.55,
                  }}
                >
                  {t.versionPicker.immediateWarnBefore}
                  <code style={{ fontFamily: 'monospace', fontWeight: 600 }}>{conflictVer}</code>
                  {t.versionPicker.immediateWarnAfter}
                </div>
              )}
            </div>
          </Col>
        </Row>

        {/* 底栏 */}
        <div
          style={{
            paddingTop: 14,
            borderTop: `1px solid ${token.colorBorderSecondary}`,
            display: 'flex',
            justifyContent: 'flex-end',
            gap: 8,
          }}
        >
          <Button onClick={onClose}>{t.versionPicker.cancel}</Button>
          <Button
            type="primary"
            loading={submitting}
            disabled={!selectedTag}
            onClick={handleSubmit}
          >
            {onPickOnly ? t.versionPicker.confirmPickOnly : t.versionPicker.confirmUpdate}
          </Button>
        </div>
    </div>
  )
}
