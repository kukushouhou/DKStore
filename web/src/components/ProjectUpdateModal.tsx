import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, Modal, Spin, message, theme } from 'antd'
import { api, type AppCardItem, type ServiceItem } from '../api/client'
import { useI18n } from '../i18n'
import { useUIStore } from '../store/ui'
import { detectComposeOverwriteRisks } from './composeGuard'
import VersionPickerModal, { type VersionPickSelection } from './VersionPickerModal'

interface ProjectUpdateModalProps {
  open: boolean
  app: AppCardItem
  initialTab?: 'direct' | 'yaml'
  onClose: () => void
  onUpdated: () => void
}

function isServiceUpdatable(svc: ServiceItem): boolean {
  return (svc.kind === 'digest_update' || svc.kind === 'version_update') && !svc.ignored
}

function isServiceCaution(svc: ServiceItem): boolean {
  return Boolean(
    svc.category === 'caution' ||
      svc.isDatabase ||
      svc.databaseName ||
      svc.roleKind === 'database' ||
      svc.roleKind === 'mq' ||
      svc.roleKind === 'middleware',
  )
}

export default function ProjectUpdateModal(props: ProjectUpdateModalProps) {
  return (
    <Modal
      open={props.open}
      onCancel={props.onClose}
      footer={null}
      width={680}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {props.open && (
        <ProjectUpdateModalBody
          key={`${props.app.key}::${props.initialTab ?? 'direct'}`}
          {...props}
        />
      )}
    </Modal>
  )
}

function ProjectUpdateModalBody({
  app,
  initialTab = 'direct',
  onClose,
  onUpdated,
}: ProjectUpdateModalProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const upsertUpdateJob = useUIStore((s) => s.upsertUpdateJob)
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  const services = useMemo(
    () => (app.allServices && app.allServices.length > 0 ? app.allServices : app.services),
    [app.allServices, app.services],
  )
  const totalCount = app.totalServiceCount ?? services.length
  const projectTitle = app.projectName || app.appName || app.name

  const [tab, setTab] = useState<'direct' | 'yaml'>(initialTab)
  const [checkedKeys, setCheckedKeys] = useState<Set<string>>(() => {
    const initSet = new Set<string>()
    for (const s of services) {
      if (isServiceUpdatable(s) && !isServiceCaution(s)) {
        initSet.add(s.key)
      }
    }
    return initSet
  })
  const [overrides, setOverrides] = useState<Record<string, VersionPickSelection>>({})
  const [pickerSvc, setPickerSvc] = useState<ServiceItem | null>(null)
  const [submittingDirect, setSubmittingDirect] = useState(false)

  // YAML 编辑状态
  const [yamlLoaded, setYamlLoaded] = useState(false)
  const [yamlEditable, setYamlEditable] = useState(true)
  const [yamlReason, setYamlReason] = useState('')
  const [origYaml, setOrigYaml] = useState('')
  const [yamlText, setYamlText] = useState('')
  const [submittingYaml, setSubmittingYaml] = useState(false)

  const loadingYaml = tab === 'yaml' && !yamlLoaded

  useEffect(() => {
    if (tab !== 'yaml' || yamlLoaded) return
    let cancelled = false
    ;(async () => {
      try {
        const res = await api.getAppCompose(app.key)
        if (cancelled) return
        setYamlEditable(res.editable)
        setYamlReason(res.reason || '')
        setOrigYaml(res.content || '')
        setYamlText(res.content || '')
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
  }, [tab, yamlLoaded, app.key])

  const updatableServices = useMemo(
    () => services.filter((s) => isServiceUpdatable(s)),
    [services],
  )

  const toggleService = (key: string) => {
    setCheckedKeys((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const selectAllUpdatable = () => {
    setCheckedKeys(new Set(updatableServices.map((s) => s.key)))
  }

  const handleDirectSubmit = async () => {
    const selected = updatableServices.filter((s) => checkedKeys.has(s.key))
    if (selected.length === 0) return
    setSubmittingDirect(true)
    try {
      const res = await api.startUpdates({
        items: selected.map((s) => {
          const ov = overrides[s.key]
          return {
            serviceKey: s.key,
            targetTag: ov?.targetTag,
            policyMode: ov?.policyMode,
            policyPrefix: ov?.policyPrefix,
            skipTargetTag: ov?.skipTargetTag,
          }
        }),
      })
      if (res.items) {
        for (const job of res.items) {
          upsertUpdateJob(job)
        }
      }
      message.info(t.home.updating)
      onUpdated()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmittingDirect(false)
    }
  }

  const handleClearAndPaste = async () => {
    try {
      if (navigator.clipboard?.readText) {
        const clip = await navigator.clipboard.readText()
        if (clip && clip.trim()) {
          setYamlText(clip)
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

  const handleYamlSubmit = async () => {
    if (!yamlText.trim()) {
      message.warning(t.projectUpdate.yamlEmptyError)
      return
    }
    setSubmittingYaml(true)
    try {
      const res = await api.updateAppCompose({ appKey: app.key, content: yamlText })
      if (res.job) {
        upsertUpdateJob(res.job)
      }
      if (res.items) {
        for (const job of res.items) {
          upsertUpdateJob(job)
        }
      }
      message.success(t.projectUpdate.yamlUpdateStarted)
      onUpdated()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmittingYaml(false)
    }
  }

  const overwriteRisks = useMemo(
    () => detectComposeOverwriteRisks(origYaml, yamlText),
    [origYaml, yamlText],
  )

  const roleBadgeForService = (svc: ServiceItem) => {
    let label = ''
    if (svc.roleKind === 'mq') {
      label = t.home.roleMq
    } else if (svc.roleKind === 'middleware') {
      label = t.home.roleMiddleware
    } else if (svc.isDatabase || svc.roleKind === 'database' || svc.databaseName) {
      label = svc.databaseName
        ? `${t.home.roleDatabase} · ${svc.databaseName}`
        : t.home.roleDatabase
    }
    if (!label) return null
    return (
      <span
        style={{
          padding: '1px 6px',
          borderRadius: 5,
          background: 'rgba(245, 158, 11, 0.15)',
          color: '#d97706',
          fontSize: 11,
          fontWeight: 500,
        }}
      >
        {label}
      </span>
    )
  }

  return (
    <>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {/* 顶栏 */}
          <div
            style={{
              display: 'flex',
              flexWrap: 'wrap',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: 12,
              paddingBottom: 12,
              borderBottom: `1px solid ${token.colorBorderSecondary}`,
            }}
          >
            <div>
              <div style={{ fontSize: 16, fontWeight: 700, color: token.colorText }}>
                {t.projectUpdate.title.replace('{name}', projectTitle)}
              </div>
              <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 2 }}>
                {t.projectUpdate.subtitle
                  .replace('{host}', app.hostName)
                  .replace('{total}', String(totalCount))}
              </div>
            </div>

            {(app.composeEditable || tab === 'yaml') && totalCount >= 2 && (
              <div
                style={{
                  display: 'inline-flex',
                  padding: 3,
                  borderRadius: 10,
                  background: token.colorFillQuaternary,
                  border: `1px solid ${token.colorBorderSecondary}`,
                  gap: 2,
                }}
              >
                <button
                  type="button"
                  onClick={() => setTab('direct')}
                  style={{
                    padding: '5px 12px',
                    borderRadius: 8,
                    border: 'none',
                    fontSize: 12,
                    fontWeight: 500,
                    cursor: 'pointer',
                    background: tab === 'direct' ? token.colorPrimary : 'transparent',
                    color: tab === 'direct' ? '#ffffff' : token.colorTextSecondary,
                  }}
                >
                  {t.projectUpdate.tabDirect}
                </button>
                <button
                  type="button"
                  onClick={() => setTab('yaml')}
                  style={{
                    padding: '5px 12px',
                    borderRadius: 8,
                    border: 'none',
                    fontSize: 12,
                    fontWeight: 500,
                    cursor: 'pointer',
                    background: tab === 'yaml' ? token.colorPrimary : 'transparent',
                    color: tab === 'yaml' ? '#ffffff' : token.colorTextSecondary,
                  }}
                >
                  {t.projectUpdate.tabYaml}
                </button>
              </div>
            )}
          </div>

          {tab === 'direct' ? (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              {/* 顶部自然提示框 */}
              <div
                style={{
                  borderRadius: 12,
                  background: 'rgba(245, 158, 11, 0.09)',
                  border: '1px solid rgba(245, 158, 11, 0.28)',
                  padding: '10px 14px',
                  fontSize: 12,
                  color: token.colorTextSecondary,
                  lineHeight: 1.6,
                }}
              >
                <strong style={{ color: '#d97706' }}>{t.projectUpdate.tipPrefix}</strong>
                {t.projectUpdate.tipDirectBody}
                {app.composeEditable ? (
                  <>
                    {t.projectUpdate.tipYamlSuffixBefore}
                    <button
                      type="button"
                      onClick={() => setTab('yaml')}
                      style={{
                        border: 'none',
                        background: 'transparent',
                        color: token.colorPrimary,
                        fontWeight: 500,
                        cursor: 'pointer',
                        padding: '0 2px',
                        fontSize: 12,
                      }}
                    >
                      {t.projectUpdate.tipYamlLink}
                    </button>
                    {t.projectUpdate.tipYamlSuffixAfter}
                  </>
                ) : (
                  t.projectUpdate.tipNoYamlEnd
                )}
              </div>

              {/* 项目内全部容器列表 */}
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
                {services.map((svc) => {
                  const updatable = isServiceUpdatable(svc)
                  const caution = isServiceCaution(svc)
                  const checked = checkedKeys.has(svc.key)
                  const ov = overrides[svc.key]
                  const displayTgt =
                    ov?.targetTag || svc.targetVersion || t.home.newImageDetected
                  const displayCur = svc.currentVersion || t.home.unknownCurrent
                  const extLink = svc.links.releases || svc.links.repo

                  if (!updatable) {
                    return (
                      <div
                        key={svc.key}
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 12,
                          padding: '11px 14px',
                          borderRadius: 12,
                          border: `1px solid ${token.colorBorderSecondary}`,
                          background: token.colorFillQuaternary,
                          opacity: 0.6,
                          fontSize: 12,
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                          <input type="checkbox" disabled style={{ width: 15, height: 15 }} />
                          <div>
                            <span style={{ fontWeight: 600, color: token.colorText }}>
                              {svc.name}
                            </span>
                            {svc.currentVersion && (
                              <span
                                style={{
                                  fontFamily: 'monospace',
                                  color: token.colorTextSecondary,
                                  marginLeft: 8,
                                }}
                              >
                                {svc.currentVersion}
                              </span>
                            )}
                          </div>
                        </div>
                        <span style={{ color: '#10b981', fontWeight: 500 }}>
                          {svc.ignored ? t.home.pausedReminderStatus : t.projectUpdate.upToDate}
                        </span>
                      </div>
                    )
                  }

                  return (
                    <div
                      key={svc.key}
                      onClick={() => toggleService(svc.key)}
                      style={{
                        display: 'flex',
                        flexWrap: 'wrap',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        gap: 12,
                        padding: '11px 14px',
                        borderRadius: 12,
                        border: checked
                          ? caution
                            ? '1px solid rgba(245, 158, 11, 0.5)'
                            : `1px solid ${token.colorPrimary}`
                          : `1px solid ${token.colorBorderSecondary}`,
                        background: token.colorFillQuaternary,
                        cursor: 'pointer',
                        fontSize: 12,
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggleService(svc.key)}
                          onClick={(e) => e.stopPropagation()}
                          style={{
                            width: 15,
                            height: 15,
                            accentColor: caution ? '#f59e0b' : token.colorPrimary,
                            cursor: 'pointer',
                          }}
                        />
                        <div>
                          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                            <span
                              style={{ fontWeight: 600, fontSize: 13, color: token.colorText }}
                            >
                              {svc.name}
                            </span>
                            {roleBadgeForService(svc)}
                          </div>
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              gap: 6,
                              marginTop: 4,
                              fontFamily: 'monospace',
                            }}
                          >
                            <span style={{ color: token.colorTextSecondary }}>{displayCur}</span>
                            <span style={{ color: token.colorTextSecondary }}>→</span>
                            <button
                              type="button"
                              title={t.home.pickOtherVersionTip}
                              className="dkstore-ver-pill"
                              onClick={(e) => {
                                e.stopPropagation()
                                setPickerSvc(svc)
                              }}
                              style={{
                                display: 'inline-flex',
                                alignItems: 'center',
                                gap: 4,
                                padding: '2px 8px',
                                borderRadius: 6,
                                fontFamily: 'monospace',
                                fontSize: 12,
                                fontWeight: 600,
                                ...(caution
                                  ? {
                                      background: 'rgba(245, 158, 11, 0.12)',
                                      border: '1px solid rgba(245, 158, 11, 0.4)',
                                      color: '#d97706',
                                    }
                                  : {
                                      background: 'rgba(47, 107, 255, 0.10)',
                                      border: '1px solid rgba(47, 107, 255, 0.35)',
                                      color: token.colorPrimary,
                                    }),
                              }}
                            >
                              <span>{displayTgt}</span>
                              <svg
                                width="12"
                                height="12"
                                viewBox="0 0 20 20"
                                fill="currentColor"
                                style={{ opacity: 0.75 }}
                              >
                                <path
                                  fillRule="evenodd"
                                  d="M5.23 7.21a.75.75 0 011.06.02L10 11.168l3.71-3.938a.75.75 0 111.08 1.04l-4.25 4.5a.75.75 0 01-1.08 0l-4.25-4.5a.75.75 0 01.02-1.06z"
                                  clipRule="evenodd"
                                />
                              </svg>
                            </button>
                          </div>
                        </div>
                      </div>

                      {extLink && (
                        <a
                          href={extLink}
                          target="_blank"
                          rel="noreferrer"
                          onClick={(e) => e.stopPropagation()}
                          style={{
                            color: token.colorPrimary,
                            fontSize: 12,
                            textDecoration: 'none',
                            flexShrink: 0,
                          }}
                        >
                          {svc.links.releases
                            ? t.projectUpdate.releaseNotesExt
                            : t.projectUpdate.repoExt}
                        </a>
                      )}
                    </div>
                  )
                })}
              </div>

              {/* 底栏 */}
              <div
                style={{
                  paddingTop: 12,
                  borderTop: `1px solid ${token.colorBorderSecondary}`,
                  display: 'flex',
                  flexWrap: 'wrap',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 8,
                }}
              >
                {updatableServices.length > 0 ? (
                  <button
                    type="button"
                    onClick={selectAllUpdatable}
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: token.colorPrimary,
                      fontSize: 12,
                      cursor: 'pointer',
                      padding: 0,
                    }}
                  >
                    {t.projectUpdate.selectAllUpdatable.replace(
                      '{count}',
                      String(updatableServices.length),
                    )}
                  </button>
                ) : (
                  <span />
                )}

                <div style={{ display: 'flex', gap: 8 }}>
                  <Button onClick={onClose}>{t.projectUpdate.cancel}</Button>
                  <Button
                    type="primary"
                    disabled={checkedKeys.size === 0}
                    loading={submittingDirect}
                    onClick={handleDirectSubmit}
                  >
                    {checkedKeys.size > 0
                      ? t.projectUpdate.submitSelected.replace(
                          '{count}',
                          String(checkedKeys.size),
                        )
                      : t.projectUpdate.selectAtLeastOne}
                  </Button>
                </div>
              </div>
            </div>
          ) : (
            /* 按官方新版配置更新模式 */
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <div
                style={{
                  borderRadius: 12,
                  background: 'rgba(47, 107, 255, 0.08)',
                  border: '1px solid rgba(47, 107, 255, 0.25)',
                  padding: '12px 14px',
                  fontSize: 12,
                  lineHeight: 1.6,
                }}
              >
                <div style={{ fontWeight: 600, color: token.colorPrimary, marginBottom: 4 }}>
                  {t.projectUpdate.yamlGuideTitle}
                </div>
                <ul
                  style={{
                    margin: 0,
                    paddingLeft: 18,
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
              </div>

              {loadingYaml ? (
                <div
                  style={{
                    height: 200,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: 8,
                    fontSize: 12,
                    color: token.colorTextSecondary,
                  }}
                >
                  <Spin size="small" />
                  <span>{t.projectUpdate.loadingCompose}</span>
                </div>
              ) : !yamlEditable ? (
                <div
                  style={{
                    borderRadius: 12,
                    padding: '16px',
                    background: token.colorFillQuaternary,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    color: token.colorTextSecondary,
                    fontSize: 12,
                  }}
                >
                  {yamlReason || t.projectUpdate.uneditableReason}
                </div>
              ) : (
                <>
                  <div
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 8,
                    }}
                  >
                    <span style={{ fontSize: 12, fontWeight: 600, color: token.colorText }}>
                      {t.projectUpdate.yamlContentLabel}
                    </span>
                    <div style={{ display: 'flex', gap: 8 }}>
                      <Button size="small" type="primary" ghost onClick={handleClearAndPaste}>
                        {t.projectUpdate.clearAndPasteBtn}
                      </Button>
                      <Button
                        size="small"
                        onClick={() => setYamlText(origYaml)}
                        disabled={yamlText === origYaml}
                      >
                        {t.projectUpdate.restoreOrigBtn}
                      </Button>
                    </div>
                  </div>

                  <textarea
                    ref={textareaRef}
                    rows={10}
                    spellCheck={false}
                    value={yamlText}
                    onChange={(e) => setYamlText(e.target.value)}
                    style={{
                      width: '100%',
                      boxSizing: 'border-box',
                      padding: 12,
                      borderRadius: 12,
                      border: `1px solid ${token.colorBorder}`,
                      background: token.colorFillQuaternary,
                      color: token.colorText,
                      fontFamily: 'monospace',
                      fontSize: 12,
                      lineHeight: 1.55,
                      resize: 'vertical',
                      outline: 'none',
                    }}
                  />

                  {overwriteRisks.missingPaths.length > 0 && (
                    <div
                      style={{
                        borderRadius: 12,
                        background: 'rgba(245, 158, 11, 0.10)',
                        border: '1px solid rgba(245, 158, 11, 0.38)',
                        padding: '10px 12px',
                        fontSize: 12,
                        color: '#d97706',
                        lineHeight: 1.55,
                      }}
                    >
                      <strong>{t.projectUpdate.warnTitle}</strong>
                      {t.projectUpdate.warnMissingPathsBefore}
                      <code style={{ fontFamily: 'monospace', fontWeight: 600 }}>
                        {overwriteRisks.missingPaths.join(', ')}
                      </code>
                      {t.projectUpdate.warnMissingPathsAfter}
                    </div>
                  )}

                  {overwriteRisks.missingPaths.length === 0 &&
                    overwriteRisks.overwrittenSecrets.length > 0 && (
                      <div
                        style={{
                          borderRadius: 12,
                          background: 'rgba(245, 158, 11, 0.10)',
                          border: '1px solid rgba(245, 158, 11, 0.38)',
                          padding: '10px 12px',
                          fontSize: 12,
                          color: '#d97706',
                          lineHeight: 1.55,
                        }}
                      >
                        <strong>{t.projectUpdate.warnTitle}</strong>
                        {t.projectUpdate.warnSecretOverwriteBefore}
                        <code style={{ fontFamily: 'monospace', fontWeight: 600 }}>
                          {overwriteRisks.overwrittenSecrets.join(', ')}
                        </code>
                        {t.projectUpdate.warnSecretOverwriteAfter}
                      </div>
                    )}
                </>
              )}

              <div
                style={{
                  paddingTop: 12,
                  borderTop: `1px solid ${token.colorBorderSecondary}`,
                  display: 'flex',
                  flexWrap: 'wrap',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 8,
                }}
              >
                <span style={{ fontSize: 12, color: token.colorTextSecondary }}>
                  {t.projectUpdate.autoRollbackNote}
                </span>
                <div style={{ display: 'flex', gap: 8 }}>
                  {totalCount >= 2 && initialTab !== 'yaml' ? (
                    <Button onClick={() => setTab('direct')}>
                      {t.projectUpdate.backToDirectBtn}
                    </Button>
                  ) : (
                    <Button onClick={onClose}>{t.projectUpdate.cancel}</Button>
                  )}
                  <Button
                    type="primary"
                    disabled={!yamlEditable || loadingYaml || !yamlText.trim()}
                    loading={submittingYaml}
                    onClick={handleYamlSubmit}
                  >
                    {t.projectUpdate.submitYamlBtn}
                  </Button>
                </div>
              </div>
            </div>
          )}
      </div>

      {/* 行内点击版本号胶囊时打开版本选择器，选定后写回该容器行 */}
      <VersionPickerModal
        open={Boolean(pickerSvc)}
        appName={pickerSvc?.name || app.name}
        subtitle={`${app.hostName}${projectTitle ? ` · ${t.home.projectSubPrefix}${projectTitle}` : ''}`}
        service={pickerSvc}
        initialSelectedTag={
          pickerSvc ? overrides[pickerSvc.key]?.targetTag || pickerSvc.targetVersion : undefined
        }
        onPickOnly={(sel) => {
          if (!pickerSvc) return
          setOverrides((prev) => ({ ...prev, [pickerSvc.key]: sel }))
          setCheckedKeys((prev) => new Set(prev).add(pickerSvc.key))
        }}
        onClose={() => setPickerSvc(null)}
      />
    </>
  )
}
