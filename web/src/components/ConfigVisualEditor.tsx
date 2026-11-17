import { useMemo, useState } from 'react'
import {
  Button,
  Card,
  Checkbox,
  Col,
  Flex,
  Input,
  Popover,
  Row,
  Segmented,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import {
  ArrowRightOutlined,
  DeleteOutlined,
  PlusOutlined,
} from '@ant-design/icons'
import { useI18n } from '../i18n'
import {
  cloneEditableConfig,
  diffConfigLists,
  normalizeNetMode,
  normalizeRestart,
  restoreServiceChangedValues,
  type EditableServiceConfig,
  type SectionDiff,
} from './configSync'

interface ConfigVisualEditorProps {
  mode: 'yaml' | 'cmd'
  textValue: string
  origTextValue: string
  origConfigs: EditableServiceConfig[]
  curConfigs: EditableServiceConfig[]
  onChangeText: (nextText: string) => void
  onChangeService: (svcName: string, nextSvc: EditableServiceConfig) => void
  onRestoreAllChangedValues: (nextServices: EditableServiceConfig[]) => void
  onResetAll: () => void
}

export default function ConfigVisualEditor({
  mode,
  textValue,
  origTextValue,
  origConfigs,
  curConfigs,
  onChangeText,
  onChangeService,
  onRestoreAllChangedValues,
  onResetAll,
}: ConfigVisualEditorProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const [selectedSvcName, setSelectedSvcName] = useState<string>('')

  const diffLabels = useMemo(
    () => ({
      diffNetLabel: t.configEditor.diffNetLabel,
      diffRestartLabel: t.configEditor.diffRestartLabel,
      diffUserLabel: t.configEditor.diffUserLabel,
      diffUserDefault: t.configEditor.diffUserDefault,
      diffPrivLabel: t.configEditor.diffPrivLabel,
      diffPrivOn: t.configEditor.diffPrivOn,
      diffPrivOff: t.configEditor.diffPrivOff,
      diffEnvPrefix: t.configEditor.diffEnvPrefix,
      diffPortPrefix: t.configEditor.diffPortPrefix,
      diffVolPrefix: t.configEditor.diffVolPrefix,
      diffDevPrefix: t.configEditor.diffDevPrefix,
      emptyVal: t.configEditor.emptyVal,
    }),
    [t.configEditor],
  )

  const diff = useMemo(
    () => diffConfigLists(origConfigs, curConfigs, diffLabels),
    [curConfigs, diffLabels, origConfigs],
  )

  const activeSvc: EditableServiceConfig | undefined = useMemo(() => {
    if (curConfigs.length === 0) return undefined
    return curConfigs.find((s) => s.name === selectedSvcName) ?? curConfigs[0]
  }, [curConfigs, selectedSvcName])

  const origActiveSvc: EditableServiceConfig | undefined = useMemo(() => {
    if (!activeSvc) return undefined
    return (
      origConfigs.find((o) => o.name === activeSvc.name) ??
      (origConfigs.length === 1 && curConfigs.length === 1 ? origConfigs[0] : undefined)
    )
  }, [activeSvc, curConfigs.length, origConfigs])

  const svcDiff = activeSvc ? diff.byService[activeSvc.name] : undefined

  const hasChangedValues = diff.changed.length > 0
  const hasAddedOrRemoved = diff.added.length > 0 || diff.removed.length > 0
  const showInfoPopover = hasChangedValues || hasAddedOrRemoved

  const warnBtnStyle: React.CSSProperties = {
    borderColor: token.colorWarning,
    color: token.colorWarning,
    background: token.colorWarningBg,
    fontWeight: 500,
  }

  const warnMiniBtnStyle: React.CSSProperties = {
    borderColor: token.colorWarning,
    color: token.colorWarning,
    background: token.colorWarningBg,
    fontSize: 11,
    height: 22,
    paddingInline: 7,
  }

  const handleRestorePersonalizedAll = () => {
    const restoredList = curConfigs.map((cs) => {
      const os =
        origConfigs.find((o) => o.name === cs.name) ??
        (origConfigs.length === 1 && curConfigs.length === 1 ? origConfigs[0] : undefined)
      return os ? restoreServiceChangedValues(os, cs) : cloneEditableConfig(cs)
    })
    onRestoreAllChangedValues(restoredList)
  }

  const handleSectionRestore = (
    section: 'runtime' | 'envs' | 'ports' | 'volumes' | 'devices',
  ) => {
    if (!activeSvc || !origActiveSvc) return
    const nextSvc = restoreServiceChangedValues(origActiveSvc, activeSvc, section)
    onChangeService(activeSvc.name, nextSvc)
  }

  const updateActiveSvc = (updater: (draft: EditableServiceConfig) => void) => {
    if (!activeSvc) return
    const draft = cloneEditableConfig(activeSvc)
    updater(draft)
    onChangeService(activeSvc.name, draft)
  }

  const renderSectionBadges = (sec?: SectionDiff) => {
    if (!sec) return null
    return (
      <>
        {sec.added.length > 0 && (
          <span
            style={{
              fontSize: 11,
              fontWeight: 500,
              color: token.colorSuccess,
              marginLeft: 4,
            }}
          >
            {t.configEditor.badgeAdded.replace('{n}', String(sec.added.length))}
          </span>
        )}
        {sec.removed.length > 0 && (
          <span
            style={{
              fontSize: 11,
              fontWeight: 500,
              color: token.colorWarning,
              marginLeft: 4,
            }}
          >
            {t.configEditor.badgeRemoved.replace('{n}', String(sec.removed.length))}
          </span>
        )}
      </>
    )
  }

  const popoverContent = (
    <div style={{ maxWidth: 360, fontSize: 12, lineHeight: 1.55 }}>
      <Typography.Text
        type="secondary"
        style={{ fontSize: 12, display: 'block', marginBottom: 6 }}
      >
        {hasChangedValues
          ? t.configEditor.tipHeaderWithChanged
          : t.configEditor.tipHeaderNoChanged}
      </Typography.Text>

      {diff.changed.length > 0 && (
        <div
          style={{
            paddingTop: 6,
            marginTop: 6,
            borderTop: `1px dashed ${token.colorSplit}`,
          }}
        >
          <div style={{ fontWeight: 600, color: token.colorWarning, marginBottom: 3 }}>
            {t.configEditor.tipChangedTitle}
          </div>
          {diff.changed.map((it, idx) => (
            <div
              key={`${it.label}-${idx}`}
              style={{
                fontFamily: token.fontFamilyCode,
                fontSize: 11.5,
                wordBreak: 'break-all',
                marginBottom: 2,
              }}
            >
              • {it.label}:{' '}
              <span style={{ textDecoration: 'line-through', opacity: 0.65 }}>{it.from}</span>{' '}
              → <strong style={{ color: token.colorWarning }}>{it.to}</strong>
            </div>
          ))}
        </div>
      )}

      {diff.added.length > 0 && (
        <div
          style={{
            paddingTop: 6,
            marginTop: 6,
            borderTop: `1px dashed ${token.colorSplit}`,
          }}
        >
          <div style={{ fontWeight: 600, color: token.colorSuccess, marginBottom: 3 }}>
            {t.configEditor.tipAddedTitle}
          </div>
          {diff.added.map((s, idx) => (
            <div
              key={`add-${idx}`}
              style={{
                fontFamily: token.fontFamilyCode,
                fontSize: 11.5,
                wordBreak: 'break-all',
                color: token.colorSuccess,
                marginBottom: 2,
              }}
            >
              + {s}
            </div>
          ))}
        </div>
      )}

      {diff.removed.length > 0 && (
        <div
          style={{
            paddingTop: 6,
            marginTop: 6,
            borderTop: `1px dashed ${token.colorSplit}`,
          }}
        >
          <div style={{ fontWeight: 600, color: token.colorWarning, marginBottom: 3 }}>
            {t.configEditor.tipRemovedTitle}
          </div>
          {diff.removed.map((s, idx) => (
            <div
              key={`rem-${idx}`}
              style={{
                fontFamily: token.fontFamilyCode,
                fontSize: 11.5,
                wordBreak: 'break-all',
                color: token.colorWarning,
                marginBottom: 2,
              }}
            >
              - {s}
            </div>
          ))}
        </div>
      )}
    </div>
  )

  const summaryAddRemoveText = [
    diff.added.length > 0
      ? t.configEditor.diffAddedPill.replace('{n}', String(diff.added.length))
      : '',
    diff.removed.length > 0
      ? t.configEditor.diffRemovedPill.replace('{n}', String(diff.removed.length))
      : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <Flex vertical gap={10}>
      {/* 顶栏：左侧标签 + 右侧【恢复个性化配置 · !提示 · 恢复全部原配置】 */}
      <Flex wrap="wrap" align="center" justify="space-between" gap={8}>
        <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
          {mode === 'yaml' ? t.configEditor.yamlBoxLabel : t.configEditor.cmdBoxLabel}
        </Typography.Text>

        <Space size={6} wrap align="center">
          {hasChangedValues && (
            <Button size="small" style={warnBtnStyle} onClick={handleRestorePersonalizedAll}>
              {t.configEditor.restorePersonalizedBtn}
            </Button>
          )}

          {!hasChangedValues && hasAddedOrRemoved && (
            <Tag
              color="warning"
              style={{
                margin: 0,
                fontSize: 12,
                fontWeight: 500,
                borderColor: token.colorWarning,
              }}
            >
              {summaryAddRemoveText}
            </Tag>
          )}

          {showInfoPopover && (
            <Popover
              placement="bottomRight"
              trigger={['hover', 'click']}
              content={popoverContent}
            >
              <span
                role="button"
                tabIndex={0}
                aria-label={t.configEditor.restorePersonalizedBtn}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  width: 18,
                  height: 18,
                  borderRadius: '50%',
                  border: `1.5px solid ${token.colorWarning}`,
                  background: token.colorWarningBg,
                  color: token.colorWarning,
                  fontSize: 12,
                  fontWeight: 700,
                  lineHeight: 1,
                  cursor: 'help',
                  userSelect: 'none',
                }}
              >
                !
              </span>
            </Popover>
          )}

          <Button
            size="small"
            disabled={textValue === origTextValue}
            onClick={onResetAll}
          >
            {t.configEditor.restoreAllBtn}
          </Button>
        </Space>
      </Flex>

      {/* 上方代码 / 命令编辑框 */}
      <Input.TextArea
        rows={6}
        spellCheck={false}
        value={textValue}
        onChange={(e) => onChangeText(e.target.value)}
        style={{
          fontFamily: token.fontFamilyCode,
          fontSize: 12.5,
          lineHeight: 1.55,
        }}
      />

      {/* 多容器项目可视化切换器 */}
      {mode === 'yaml' && curConfigs.length > 1 && activeSvc && (
        <Flex align="center" gap={8} wrap="wrap">
          <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
            {t.configEditor.servicePickerLabel}
          </Typography.Text>
          <Segmented
            size="small"
            value={activeSvc.name}
            onChange={(val) => setSelectedSvcName(String(val))}
            options={curConfigs.map((s) => ({ label: s.name, value: s.name }))}
          />
        </Flex>
      )}

      {/* 下方可视化表单区（与上方文本实时双向联动） */}
      {activeSvc && (
        <>
          {/* 紧凑运行参数栏：网络 / 重启策略 / 运行用户 / 特权模式 */}
          <Flex
            wrap="wrap"
            align="center"
            gap="6px 10px"
            style={{
              padding: '7px 10px',
              borderRadius: token.borderRadius,
              border: `1px solid ${
                (svcDiff?.runtime.length ?? 0) > 0
                  ? token.colorWarningBorder
                  : token.colorBorderSecondary
              }`,
              background: token.colorFillAlter,
            }}
          >
            <Space size={4} align="center">
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t.configEditor.networkLabel}
              </Typography.Text>
              <Input
                size="small"
                value={activeSvc.networkMode}
                placeholder={t.configEditor.networkPlaceholder}
                style={{
                  width: 108,
                  fontFamily: token.fontFamilyCode,
                  fontSize: 12,
                  borderColor:
                    origActiveSvc &&
                    normalizeNetMode(origActiveSvc.networkMode) !==
                      normalizeNetMode(activeSvc.networkMode)
                      ? token.colorWarning
                      : undefined,
                }}
                onChange={(e) => {
                  const val = e.target.value
                  updateActiveSvc((d) => {
                    d.networkMode = val
                  })
                }}
              />
            </Space>

            <Space size={4} align="center">
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t.configEditor.restartLabel}
              </Typography.Text>
              <Select
                size="small"
                value={normalizeRestart(activeSvc.restartPolicy) || 'no'}
                style={{ width: 130 }}
                options={[
                  { label: t.configEditor.restartNo, value: 'no' },
                  { label: 'always', value: 'always' },
                  { label: 'unless-stopped', value: 'unless-stopped' },
                  { label: 'on-failure', value: 'on-failure' },
                ]}
                onChange={(val) => {
                  updateActiveSvc((d) => {
                    d.restartPolicy = val === 'no' ? '' : val
                  })
                }}
              />
            </Space>

            <Space size={4} align="center">
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t.configEditor.userLabel}
              </Typography.Text>
              <Input
                size="small"
                value={activeSvc.user}
                placeholder={t.configEditor.userPlaceholder}
                style={{
                  width: 78,
                  fontFamily: token.fontFamilyCode,
                  fontSize: 12,
                  borderColor:
                    origActiveSvc && origActiveSvc.user.trim() !== activeSvc.user.trim()
                      ? token.colorWarning
                      : undefined,
                }}
                onChange={(e) => {
                  const val = e.target.value
                  updateActiveSvc((d) => {
                    d.user = val
                  })
                }}
              />
            </Space>

            <Checkbox
              checked={activeSvc.privileged}
              onChange={(e) => {
                const checked = e.target.checked
                updateActiveSvc((d) => {
                  d.privileged = checked
                })
              }}
              style={{ fontSize: 12 }}
            >
              <span
                style={{
                  fontSize: 12,
                  color:
                    origActiveSvc &&
                    Boolean(origActiveSvc.privileged) !== Boolean(activeSvc.privileged)
                      ? token.colorWarning
                      : undefined,
                }}
              >
                {t.configEditor.privilegedLabel}
              </span>
            </Checkbox>

            {(svcDiff?.runtime.length ?? 0) > 0 && (
              <Button
                size="small"
                style={{ ...warnMiniBtnStyle, marginLeft: 'auto' }}
                onClick={() => handleSectionRestore('runtime')}
              >
                {t.configEditor.restoreRuntimeBtn}
              </Button>
            )}
          </Flex>

          {/* 2x2 网格：环境变量 · 端口映射 · 目录挂载 · 硬件直通 */}
          <Row gutter={[10, 10]}>
            {/* 1. 环境变量 */}
            <Col xs={24} md={12}>
              <Card
                size="small"
                styles={{
                  header: { minHeight: 36, padding: '0 10px', fontSize: 12.5 },
                  body: { padding: '8px 10px' },
                }}
                style={{
                  height: '100%',
                  borderColor:
                    (svcDiff?.envs.changed.length ?? 0) > 0
                      ? token.colorWarningBorder
                      : undefined,
                }}
                title={
                  <span>
                    {t.configEditor.envTitle}
                    {renderSectionBadges(svcDiff?.envs)}
                  </span>
                }
                extra={
                  <Space size={6}>
                    {(svcDiff?.envs.changed.length ?? 0) > 0 && (
                      <Button
                        size="small"
                        style={warnMiniBtnStyle}
                        onClick={() => handleSectionRestore('envs')}
                      >
                        {t.configEditor.restoreEnvBtn}
                      </Button>
                    )}
                    <Button
                      size="small"
                      type="primary"
                      ghost
                      icon={<PlusOutlined />}
                      style={{ fontSize: 12, height: 22, paddingInline: 7 }}
                      onClick={() =>
                        updateActiveSvc((d) => {
                          d.envs.push({ key: '', value: '' })
                        })
                      }
                    >
                      {t.configEditor.addEnvShort}
                    </Button>
                  </Space>
                }
              >
                {activeSvc.envs.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t.configEditor.envEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={5} style={{ maxHeight: 132, overflowY: 'auto' }}>
                    {activeSvc.envs.map((env, idx) => {
                      const origItem = origActiveSvc?.envs.find(
                        (x) => x.key.trim() && x.key.trim() === env.key.trim(),
                      )
                      const isValChanged = Boolean(origItem && origItem.value !== env.value)
                      return (
                        <Flex key={idx} align="center" gap={4}>
                          <Input
                            size="small"
                            value={env.key}
                            placeholder={t.configEditor.envKeyPh}
                            style={{
                              flex: '0 1 42%',
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.envs[idx] = { ...d.envs[idx], key: val }
                              })
                            }}
                          />
                          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                            =
                          </Typography.Text>
                          <Input
                            size="small"
                            value={env.value}
                            placeholder={t.configEditor.envValPh}
                            style={{
                              flex: 1,
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                              borderColor: isValChanged ? token.colorWarning : undefined,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.envs[idx] = { ...d.envs[idx], value: val }
                              })
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
                                updateActiveSvc((d) => {
                                  d.envs.splice(idx, 1)
                                })
                              }
                            />
                          </Tooltip>
                        </Flex>
                      )
                    })}
                  </Flex>
                )}
              </Card>
            </Col>

            {/* 2. 端口映射 */}
            <Col xs={24} md={12}>
              <Card
                size="small"
                styles={{
                  header: { minHeight: 36, padding: '0 10px', fontSize: 12.5 },
                  body: { padding: '8px 10px' },
                }}
                style={{
                  height: '100%',
                  borderColor:
                    (svcDiff?.ports.changed.length ?? 0) > 0
                      ? token.colorWarningBorder
                      : undefined,
                }}
                title={
                  <span>
                    {t.configEditor.portTitle}
                    {renderSectionBadges(svcDiff?.ports)}
                  </span>
                }
                extra={
                  <Space size={6}>
                    {(svcDiff?.ports.changed.length ?? 0) > 0 && (
                      <Button
                        size="small"
                        style={warnMiniBtnStyle}
                        onClick={() => handleSectionRestore('ports')}
                      >
                        {t.configEditor.restorePortBtn}
                      </Button>
                    )}
                    <Button
                      size="small"
                      type="primary"
                      ghost
                      icon={<PlusOutlined />}
                      style={{ fontSize: 12, height: 22, paddingInline: 7 }}
                      onClick={() =>
                        updateActiveSvc((d) => {
                          d.ports.push({ hostPort: '', containerPort: '', protocol: 'tcp' })
                        })
                      }
                    >
                      {t.configEditor.addPortShort}
                    </Button>
                  </Space>
                }
              >
                {activeSvc.ports.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {normalizeNetMode(activeSvc.networkMode) === 'host'
                      ? t.configEditor.portHostModeHint
                      : t.configEditor.portEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={5} style={{ maxHeight: 132, overflowY: 'auto' }}>
                    {activeSvc.ports.map((pt, idx) => {
                      const proto = (pt.protocol || 'tcp').toLowerCase()
                      const origItem = origActiveSvc?.ports.find(
                        (x) =>
                          x.containerPort.trim() &&
                          x.containerPort.trim() === pt.containerPort.trim() &&
                          (x.protocol || 'tcp').toLowerCase() === proto,
                      )
                      const isValChanged = Boolean(
                        origItem && origItem.hostPort.trim() !== pt.hostPort.trim(),
                      )
                      return (
                        <Flex key={idx} align="center" gap={4}>
                          <Input
                            size="small"
                            value={pt.hostPort}
                            placeholder={t.configEditor.portHostPh}
                            style={{
                              flex: 1,
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                              borderColor: isValChanged ? token.colorWarning : undefined,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.ports[idx] = { ...d.ports[idx], hostPort: val }
                              })
                            }}
                          />
                          <ArrowRightOutlined
                            style={{ fontSize: 10, color: token.colorTextTertiary }}
                          />
                          <Input
                            size="small"
                            value={pt.containerPort}
                            placeholder={t.configEditor.portCtrPh}
                            style={{
                              flex: 1,
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.ports[idx] = { ...d.ports[idx], containerPort: val }
                              })
                            }}
                          />
                          <Select
                            size="small"
                            value={proto}
                            style={{ width: 72 }}
                            options={[
                              { label: 'TCP', value: 'tcp' },
                              { label: 'UDP', value: 'udp' },
                            ]}
                            onChange={(val) =>
                              updateActiveSvc((d) => {
                                d.ports[idx] = { ...d.ports[idx], protocol: val }
                              })
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
                                updateActiveSvc((d) => {
                                  d.ports.splice(idx, 1)
                                })
                              }
                            />
                          </Tooltip>
                        </Flex>
                      )
                    })}
                  </Flex>
                )}
              </Card>
            </Col>

            {/* 3. 目录挂载 */}
            <Col xs={24} md={12}>
              <Card
                size="small"
                styles={{
                  header: { minHeight: 36, padding: '0 10px', fontSize: 12.5 },
                  body: { padding: '8px 10px' },
                }}
                style={{
                  height: '100%',
                  borderColor:
                    (svcDiff?.volumes.changed.length ?? 0) > 0
                      ? token.colorWarningBorder
                      : undefined,
                }}
                title={
                  <span>
                    {t.configEditor.volTitle}
                    {renderSectionBadges(svcDiff?.volumes)}
                  </span>
                }
                extra={
                  <Space size={6}>
                    {(svcDiff?.volumes.changed.length ?? 0) > 0 && (
                      <Button
                        size="small"
                        style={warnMiniBtnStyle}
                        onClick={() => handleSectionRestore('volumes')}
                      >
                        {t.configEditor.restoreVolBtn}
                      </Button>
                    )}
                    <Button
                      size="small"
                      type="primary"
                      ghost
                      icon={<PlusOutlined />}
                      style={{ fontSize: 12, height: 22, paddingInline: 7 }}
                      onClick={() =>
                        updateActiveSvc((d) => {
                          d.volumes.push({ hostPath: '', containerPath: '', mode: 'rw' })
                        })
                      }
                    >
                      {t.configEditor.addVolShort}
                    </Button>
                  </Space>
                }
              >
                {activeSvc.volumes.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t.configEditor.volEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={5} style={{ maxHeight: 132, overflowY: 'auto' }}>
                    {activeSvc.volumes.map((vol, idx) => {
                      const origItem = origActiveSvc?.volumes.find(
                        (x) =>
                          x.containerPath.trim() &&
                          x.containerPath.trim() === vol.containerPath.trim(),
                      )
                      const isValChanged = Boolean(
                        origItem && origItem.hostPath.trim() !== vol.hostPath.trim(),
                      )
                      return (
                        <Flex key={idx} align="center" gap={4}>
                          <Input
                            size="small"
                            value={vol.hostPath}
                            placeholder={t.configEditor.volHostPh}
                            style={{
                              flex: '1 1 54%',
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                              borderColor: isValChanged ? token.colorWarning : undefined,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.volumes[idx] = { ...d.volumes[idx], hostPath: val }
                              })
                            }}
                          />
                          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                            :
                          </Typography.Text>
                          <Input
                            size="small"
                            value={vol.containerPath}
                            placeholder={t.configEditor.volCtrPh}
                            style={{
                              flex: '1 1 46%',
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.volumes[idx] = { ...d.volumes[idx], containerPath: val }
                              })
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
                                updateActiveSvc((d) => {
                                  d.volumes.splice(idx, 1)
                                })
                              }
                            />
                          </Tooltip>
                        </Flex>
                      )
                    })}
                  </Flex>
                )}
              </Card>
            </Col>

            {/* 4. 硬件直通 */}
            <Col xs={24} md={12}>
              <Card
                size="small"
                styles={{
                  header: { minHeight: 36, padding: '0 10px', fontSize: 12.5 },
                  body: { padding: '8px 10px' },
                }}
                style={{
                  height: '100%',
                  borderColor:
                    (svcDiff?.devices.changed.length ?? 0) > 0
                      ? token.colorWarningBorder
                      : undefined,
                }}
                title={
                  <span>
                    {t.configEditor.devTitle}{' '}
                    <Typography.Text
                      type="secondary"
                      style={{ fontSize: 11, fontWeight: 400 }}
                    >
                      {t.configEditor.devSub}
                    </Typography.Text>
                    {renderSectionBadges(svcDiff?.devices)}
                  </span>
                }
                extra={
                  <Space size={6}>
                    {(svcDiff?.devices.changed.length ?? 0) > 0 && (
                      <Button
                        size="small"
                        style={warnMiniBtnStyle}
                        onClick={() => handleSectionRestore('devices')}
                      >
                        {t.configEditor.restoreDevBtn}
                      </Button>
                    )}
                    <Button
                      size="small"
                      type="primary"
                      ghost
                      icon={<PlusOutlined />}
                      style={{ fontSize: 12, height: 22, paddingInline: 7 }}
                      onClick={() =>
                        updateActiveSvc((d) => {
                          d.devices.push({ hostPath: '', containerPath: '' })
                        })
                      }
                    >
                      {t.configEditor.addDevShort}
                    </Button>
                  </Space>
                }
              >
                {activeSvc.devices.length === 0 ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t.configEditor.devEmpty}
                  </Typography.Text>
                ) : (
                  <Flex vertical gap={5} style={{ maxHeight: 132, overflowY: 'auto' }}>
                    {activeSvc.devices.map((dev, idx) => {
                      const target = (dev.containerPath || dev.hostPath).trim()
                      const origItem = origActiveSvc?.devices.find(
                        (x) =>
                          (x.containerPath || x.hostPath).trim() &&
                          (x.containerPath || x.hostPath).trim() === target,
                      )
                      const isValChanged = Boolean(
                        origItem && origItem.hostPath.trim() !== dev.hostPath.trim(),
                      )
                      return (
                        <Flex key={idx} align="center" gap={4}>
                          <Input
                            size="small"
                            value={dev.hostPath}
                            placeholder={t.configEditor.devHostPh}
                            style={{
                              flex: 1,
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                              borderColor: isValChanged ? token.colorWarning : undefined,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.devices[idx] = { ...d.devices[idx], hostPath: val }
                              })
                            }}
                          />
                          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                            :
                          </Typography.Text>
                          <Input
                            size="small"
                            value={dev.containerPath}
                            placeholder={t.configEditor.devCtrPh}
                            style={{
                              flex: 1,
                              fontFamily: token.fontFamilyCode,
                              fontSize: 12,
                            }}
                            onChange={(e) => {
                              const val = e.target.value
                              updateActiveSvc((d) => {
                                d.devices[idx] = { ...d.devices[idx], containerPath: val }
                              })
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
                                updateActiveSvc((d) => {
                                  d.devices.splice(idx, 1)
                                })
                              }
                            />
                          </Tooltip>
                        </Flex>
                      )
                    })}
                  </Flex>
                )}
              </Card>
            </Col>
          </Row>
        </>
      )}
    </Flex>
  )
}
