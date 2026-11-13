import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfigProvider,
  Flex,
  Input,
  Modal,
  Segmented,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import type { TextAreaRef } from 'antd/es/input/TextArea'
import { ArrowRightOutlined, DownOutlined } from '@ant-design/icons'
import { api, showApiError, type AppCardItem, type ServiceItem } from '../api/client'
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
  const textareaRef = useRef<TextAreaRef | null>(null)

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
      showApiError(e)
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
      showApiError(e)
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
      <Tag variant="filled" color="warning" style={{ margin: 0 }}>
        {label}
      </Tag>
    )
  }

  return (
    <>
      <Flex vertical gap={16}>
        {/* 顶栏 */}
        <Flex
          wrap="wrap"
          align="center"
          justify="space-between"
          gap={12}
          style={{
            paddingBottom: 12,
            borderBottom: `1px solid ${token.colorSplit}`,
          }}
        >
          <div>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {t.projectUpdate.title.replace('{name}', projectTitle)}
            </Typography.Title>
            <Typography.Text
              type="secondary"
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
            >
              {t.projectUpdate.subtitle
                .replace('{host}', app.hostName)
                .replace('{total}', String(totalCount))}
            </Typography.Text>
          </div>

          {(app.composeEditable || tab === 'yaml') && totalCount >= 2 && (
            <Segmented
              value={tab}
              onChange={(val) => setTab(val as 'direct' | 'yaml')}
              options={[
                { label: t.projectUpdate.tabDirect, value: 'direct' },
                { label: t.projectUpdate.tabYaml, value: 'yaml' },
              ]}
            />
          )}
        </Flex>

        {tab === 'direct' ? (
          <Flex vertical gap={14}>
            {/* 顶部自然提示框 */}
            <Alert
              type="warning"
              showIcon
              title={
                <Typography.Text style={{ fontSize: token.fontSizeSM }}>
                  <strong>{t.projectUpdate.tipPrefix}</strong>
                  {t.projectUpdate.tipDirectBody}
                  {app.composeEditable ? (
                    <>
                      {t.projectUpdate.tipYamlSuffixBefore}
                      <Typography.Link
                        onClick={() => setTab('yaml')}
                        style={{ fontSize: token.fontSizeSM, marginInline: 2 }}
                      >
                        {t.projectUpdate.tipYamlLink}
                      </Typography.Link>
                      {t.projectUpdate.tipYamlSuffixAfter}
                    </>
                  ) : (
                    t.projectUpdate.tipNoYamlEnd
                  )}
                </Typography.Text>
              }
            />

            {/* 项目内全部容器列表 */}
            <Flex vertical gap={10}>
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
                    <Flex
                      key={svc.key}
                      align="center"
                      justify="space-between"
                      gap={12}
                      style={{
                        padding: '10px 14px',
                        borderRadius: token.borderRadiusLG,
                        border: `1px solid ${token.colorBorderSecondary}`,
                        background: token.colorFillAlter,
                        opacity: 0.65,
                      }}
                    >
                      <Space size={10} align="center">
                        <Checkbox disabled />
                        <Typography.Text strong>{svc.name}</Typography.Text>
                        {svc.state && svc.state !== 'running' && (
                          <Tag
                            variant="filled"
                            style={{
                              margin: 0,
                              display: 'inline-flex',
                              alignItems: 'center',
                              gap: 5,
                            }}
                          >
                            <Badge status="default" />
                            <span>{t.home.tagNotRunning}</span>
                          </Tag>
                        )}
                        {svc.currentVersion && (
                          <Tag style={{ margin: 0, fontFamily: token.fontFamilyCode }}>
                            {svc.currentVersion}
                          </Tag>
                        )}
                      </Space>
                      <Tag
                        color={svc.ignored ? 'default' : 'success'}
                        variant="filled"
                        style={{ margin: 0 }}
                      >
                        {svc.ignored ? t.home.pausedReminderStatus : t.projectUpdate.upToDate}
                      </Tag>
                    </Flex>
                  )
                }

                return (
                  <Flex
                    key={svc.key}
                    wrap="wrap"
                    align="center"
                    justify="space-between"
                    gap={12}
                    onClick={() => toggleService(svc.key)}
                    style={{
                      padding: '11px 14px',
                      borderRadius: token.borderRadiusLG,
                      border: `1px solid ${
                        checked
                          ? caution
                            ? token.colorWarning
                            : token.colorPrimary
                          : token.colorBorderSecondary
                      }`,
                      background: checked
                        ? caution
                          ? token.colorWarningBg
                          : token.colorPrimaryBg
                        : token.colorBgContainer,
                      cursor: 'pointer',
                      transition: 'border-color 0.15s ease, background-color 0.15s ease',
                    }}
                  >
                    <Flex align="center" gap={12}>
                      <Checkbox
                        checked={checked}
                        onChange={() => toggleService(svc.key)}
                        onClick={(e) => e.stopPropagation()}
                      />
                      <div>
                        <Space size={6} align="center" wrap>
                          <Typography.Text strong>{svc.name}</Typography.Text>
                          {svc.state && svc.state !== 'running' && (
                            <Tag
                              variant="filled"
                              style={{
                                margin: 0,
                                display: 'inline-flex',
                                alignItems: 'center',
                                gap: 5,
                              }}
                            >
                              <Badge status="default" />
                              <span>{t.home.tagNotRunning}</span>
                            </Tag>
                          )}
                          {roleBadgeForService(svc)}
                        </Space>
                        <div style={{ marginTop: 4 }}>
                          <Space size={6} align="center" wrap>
                            <Tag style={{ margin: 0, fontFamily: token.fontFamilyCode }}>
                              {displayCur}
                            </Tag>
                            <ArrowRightOutlined
                              style={{ fontSize: 11, color: token.colorTextTertiary }}
                            />
                            <ConfigProvider
                              theme={caution ? { token: { colorPrimary: token.colorWarning } } : undefined}
                            >
                              <Tooltip title={t.home.pickOtherVersionTip}>
                                <Button
                                  size="small"
                                  type="primary"
                                  ghost
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    setPickerSvc(svc)
                                  }}
                                  style={{
                                    fontFamily: token.fontFamilyCode,
                                    fontWeight: 600,
                                  }}
                                >
                                  {displayTgt} <DownOutlined style={{ fontSize: 10 }} />
                                </Button>
                              </Tooltip>
                            </ConfigProvider>
                          </Space>
                        </div>
                      </div>
                    </Flex>

                    {extLink && (
                      <Typography.Link
                        href={extLink}
                        target="_blank"
                        onClick={(e) => e.stopPropagation()}
                        style={{ fontSize: token.fontSizeSM, flexShrink: 0 }}
                      >
                        {svc.links.releases
                          ? t.projectUpdate.releaseNotesExt
                          : t.projectUpdate.repoExt}
                      </Typography.Link>
                    )}
                  </Flex>
                )
              })}
            </Flex>

            {/* 底栏 */}
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
              {updatableServices.length > 0 ? (
                <Button
                  type="link"
                  size="small"
                  onClick={selectAllUpdatable}
                  style={{ padding: 0 }}
                >
                  {t.projectUpdate.selectAllUpdatable.replace(
                    '{count}',
                    String(updatableServices.length),
                  )}
                </Button>
              ) : (
                <span />
              )}

              <Space size={8}>
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
              </Space>
            </Flex>
          </Flex>
        ) : (
          /* 按官方新版配置更新模式 */
          <Flex vertical gap={14}>
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

            {loadingYaml ? (
              <Flex align="center" justify="center" gap={8} style={{ height: 200 }}>
                <Spin size="small" />
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  {t.projectUpdate.loadingCompose}
                </Typography.Text>
              </Flex>
            ) : !yamlEditable ? (
              <Alert
                type="info"
                showIcon
                title={yamlReason || t.projectUpdate.uneditableReason}
              />
            ) : (
              <>
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
                      onClick={() => setYamlText(origYaml)}
                      disabled={yamlText === origYaml}
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
            )}

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
              </Space>
            </Flex>
          </Flex>
        )}
      </Flex>

      {/* 行内点击版本号胶囊时打开版本选择器，选定后写回该容器行 */}
      <VersionPickerModal
        open={Boolean(pickerSvc)}
        appName={pickerSvc?.name || app.name}
        subtitle={`${app.hostName}${projectTitle ? ` · ${t.home.projectSubPrefix}${projectTitle}` : ''}`}
        service={pickerSvc}
        initialSelectedTag={pickerSvc ? overrides[pickerSvc.key]?.targetTag : undefined}
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
