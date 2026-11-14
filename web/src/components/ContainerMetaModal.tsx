import { useMemo, useState } from 'react'
import {
  Button,
  Col,
  Flex,
  Input,
  Modal,
  Row,
  Segmented,
  Select,
  Tag,
  Typography,
  message,
  theme,
} from 'antd'
import { useQueryClient } from '@tanstack/react-query'
import { api, showApiError, type LocalMeta, type SaveMetaItem } from '../api/client'
import { useI18n } from '../i18n'
import AppIconAvatar from './AppIconAvatar'
import {
  defaultSchemeForPort,
  effectiveHostAddress,
  pickUserTagColor,
  type MetaModalContext,
  type MetaScopeOption,
} from './metaUtils'

interface ContainerMetaModalProps {
  open: boolean
  context: MetaModalContext | null
  allTags?: string[]
  onClose: () => void
  onSaved?: () => void
}

interface DraftMeta {
  alias: string
  tags: string[]
  accessMode: 'auto' | 'custom' | 'none'
  scheme: 'http' | 'https'
  fixedPort: string
  customUrl: string
  note: string
}

function buildInitialDraft(scope: MetaScopeOption): DraftMeta {
  const m: LocalMeta = scope.initialMeta ?? {}
  const firstPort = scope.ports[0]?.port
  const fixedPort =
    (m.fixedPort && scope.ports.some((p) => p.port.hostPort === m.fixedPort)
      ? m.fixedPort
      : firstPort?.hostPort) || ''
  const matchedPort = scope.ports.find((p) => p.port.hostPort === fixedPort)?.port ?? firstPort
  const defaultScheme = matchedPort
    ? defaultSchemeForPort(matchedPort.hostPort, matchedPort.containerPort)
    : 'http'
  const scheme = m.scheme === 'http' || m.scheme === 'https' ? m.scheme : defaultScheme
  const accessMode =
    m.accessMode === 'custom' || m.accessMode === 'none' ? m.accessMode : 'auto'

  return {
    alias: m.alias ?? '',
    tags: Array.isArray(m.tags) ? [...m.tags] : [],
    accessMode,
    scheme,
    fixedPort,
    customUrl: m.customUrl ?? '',
    note: m.note ?? '',
  }
}

function hasCustomDraft(d: DraftMeta, scope: MetaScopeOption): boolean {
  const firstPort = scope.ports[0]?.port
  const defScheme = firstPort
    ? defaultSchemeForPort(firstPort.hostPort, firstPort.containerPort)
    : 'http'
  const hasPortOverride =
    d.accessMode === 'auto' &&
    scope.ports.length > 0 &&
    ((d.fixedPort && d.fixedPort !== firstPort?.hostPort) || d.scheme !== defScheme)

  return Boolean(
    d.alias.trim() ||
      d.tags.length > 0 ||
      d.accessMode !== 'auto' ||
      hasPortOverride ||
      d.note.trim(),
  )
}

export default function ContainerMetaModal({
  open,
  context,
  allTags = [],
  onClose,
  onSaved,
}: ContainerMetaModalProps) {
  return (
    <Modal
      open={open && Boolean(context)}
      onCancel={onClose}
      footer={null}
      width={500}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {open && context && (
        <ContainerMetaModalBody
          context={context}
          allTags={allTags}
          onClose={onClose}
          onSaved={onSaved}
        />
      )}
    </Modal>
  )
}

function ContainerMetaModalBody({
  context,
  allTags,
  onClose,
  onSaved,
}: {
  context: MetaModalContext
  allTags: string[]
  onClose: () => void
  onSaved?: () => void
}) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const qc = useQueryClient()

  const [scopeId, setScopeId] = useState<string>(context.defaultScopeId)
  const [submitting, setSubmitting] = useState(false)
  const [drafts, setDrafts] = useState<Record<string, DraftMeta>>(() => {
    const map: Record<string, DraftMeta> = {}
    for (const sc of context.scopes) {
      map[sc.scopeId] = buildInitialDraft(sc)
    }
    return map
  })

  const activeScope = useMemo(
    () => context.scopes.find((s) => s.scopeId === scopeId) ?? context.scopes[0],
    [context.scopes, scopeId],
  )
  const activeDraft = drafts[activeScope.scopeId] ?? buildInitialDraft(activeScope)
  const hostAddr = effectiveHostAddress(context.hostAddress)

  const updateActiveDraft = (patch: Partial<DraftMeta>) => {
    setDrafts((prev) => ({
      ...prev,
      [activeScope.scopeId]: {
        ...(prev[activeScope.scopeId] ?? buildInitialDraft(activeScope)),
        ...patch,
      },
    }))
  }

  const tagOptions = useMemo(() => {
    const set = new Set<string>(allTags)
    for (const d of Object.values(drafts)) {
      for (const tg of d.tags) {
        if (tg.trim()) set.add(tg.trim())
      }
    }
    return Array.from(set).map((v) => ({ label: v, value: v }))
  }, [allTags, drafts])

  const handleSave = async () => {
    setSubmitting(true)
    try {
      const payload: SaveMetaItem[] = context.scopes.map((sc) => {
        const d = drafts[sc.scopeId] ?? buildInitialDraft(sc)
        const firstPort = sc.ports[0]?.port
        const matchedPort =
          sc.ports.find((p) => p.port.hostPort === d.fixedPort)?.port ?? firstPort
        const defScheme = matchedPort
          ? defaultSchemeForPort(matchedPort.hostPort, matchedPort.containerPort)
          : 'http'

        const keepPort =
          d.accessMode === 'auto' &&
          sc.ports.length > 0 &&
          d.fixedPort &&
          d.fixedPort !== firstPort?.hostPort
            ? d.fixedPort
            : ''
        const keepScheme =
          d.accessMode === 'auto' && sc.ports.length > 0 && d.scheme !== defScheme ? d.scheme : ''

        return {
          targetKey: sc.targetKey,
          hostId: context.hostId,
          meta: {
            alias: d.alias.trim(),
            tags: d.tags.map((x) => x.trim()).filter(Boolean),
            accessMode: d.accessMode,
            scheme: keepScheme,
            fixedPort: keepPort,
            customUrl: d.accessMode === 'custom' ? d.customUrl.trim() : '',
            note: d.note.trim(),
          },
        }
      })

      await api.saveMeta(payload)
      await Promise.all([
        qc.invalidateQueries({ queryKey: ['overview'] }),
        qc.invalidateQueries({ queryKey: ['containers-cached'] }),
        qc.invalidateQueries({ queryKey: ['containers-fast'] }),
        qc.invalidateQueries({ queryKey: ['containers'] }),
      ])
      message.success(t.metaModal.savedMsg)
      onSaved?.()
      onClose()
    } catch (e) {
      showApiError(e)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Flex vertical gap={14}>
      {/* 顶栏 */}
      <Flex
        align="center"
        gap={12}
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <AppIconAvatar
          name={activeDraft.alias.trim() || activeScope.rawName}
          fallbackColorSeed={activeScope.rawName}
          iconUrl={activeScope.iconUrl}
          size={38}
        />
        <div style={{ minWidth: 0, flex: 1 }}>
          <Typography.Title level={5} style={{ margin: 0 }}>
            {t.metaModal.title}
          </Typography.Title>
          <Typography.Text
            type="secondary"
            ellipsis
            style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
          >
            {context.hostName} ·{' '}
            <span style={{ fontFamily: token.fontFamilyCode }}>{activeScope.rawName}</span>
          </Typography.Text>
        </div>
      </Flex>

      {/* 多容器项目范围切换 */}
      {context.scopes.length > 1 && (
        <div style={{ overflowX: 'auto' }}>
          <Segmented
            block
            value={activeScope.scopeId}
            onChange={(val) => setScopeId(String(val))}
            options={context.scopes.map((sc) => {
              const d = drafts[sc.scopeId] ?? buildInitialDraft(sc)
              const customized = hasCustomDraft(d, sc)
              return {
                value: sc.scopeId,
                label: (
                  <Flex align="center" justify="center" gap={5}>
                    <span>{sc.label}</span>
                    {customized && (
                      <span
                        style={{
                          width: 6,
                          height: 6,
                          borderRadius: '50%',
                          backgroundColor: token.colorPrimary,
                          display: 'inline-block',
                          flexShrink: 0,
                        }}
                      />
                    )}
                  </Flex>
                ),
              }
            })}
          />
        </div>
      )}

      {/* 备注名 & 标签 */}
      <Row gutter={12}>
        <Col xs={24} sm={11}>
          <Typography.Text
            strong
            style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 6 }}
          >
            {t.metaModal.aliasLabel}
          </Typography.Text>
          <Input
            allowClear
            value={activeDraft.alias}
            placeholder={activeScope.rawName}
            onChange={(e) => updateActiveDraft({ alias: e.target.value })}
          />
        </Col>
        <Col xs={24} sm={13} style={{ marginTop: 0 }}>
          <Typography.Text
            strong
            style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 6 }}
          >
            {t.metaModal.tagsLabel}
          </Typography.Text>
          <Select
            mode="tags"
            style={{ width: '100%' }}
            value={activeDraft.tags}
            placeholder={t.metaModal.tagsPlaceholder}
            tokenSeparators={[',', '，']}
            options={tagOptions}
            onChange={(vals) => updateActiveDraft({ tags: vals })}
            tagRender={({ label, value, closable, onClose: onTagClose }) => (
              <Tag
                variant="filled"
                color={pickUserTagColor(String(value))}
                closable={closable}
                onClose={onTagClose}
                style={{ marginInlineEnd: 4 }}
              >
                {label}
              </Tag>
            )}
          />
        </Col>
      </Row>

      {/* 访问入口 */}
      <div>
        <Flex align="center" justify="space-between" gap={8} style={{ marginBottom: 8 }}>
          <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
            {t.metaModal.accessLabel}
          </Typography.Text>
          <Segmented
            size="small"
            value={activeDraft.accessMode}
            onChange={(v) =>
              updateActiveDraft({ accessMode: v as 'auto' | 'custom' | 'none' })
            }
            options={[
              { label: t.metaModal.modeAuto, value: 'auto' },
              { label: t.metaModal.modeCustom, value: 'custom' },
              { label: t.metaModal.modeNone, value: 'none' },
            ]}
          />
        </Flex>

        {activeDraft.accessMode === 'auto' &&
          (activeScope.ports.length > 0 ? (
            <Flex gap={8}>
              <Select
                value={activeDraft.scheme}
                onChange={(val) => updateActiveDraft({ scheme: val })}
                style={{ width: 108, fontFamily: token.fontFamilyCode }}
                options={[
                  { label: 'http://', value: 'http' },
                  { label: 'https://', value: 'https' },
                ]}
              />
              <Select
                value={activeDraft.fixedPort || activeScope.ports[0].port.hostPort}
                onChange={(portVal) => {
                  const found = activeScope.ports.find((p) => p.port.hostPort === portVal)
                  const nextScheme = found
                    ? defaultSchemeForPort(found.port.hostPort, found.port.containerPort)
                    : activeDraft.scheme
                  updateActiveDraft({ fixedPort: portVal, scheme: nextScheme })
                }}
                style={{ flex: 1, fontFamily: token.fontFamilyCode }}
                options={activeScope.ports.map((item) => {
                  const suffix = activeScope.isProject
                    ? `(${item.ownerName})`
                    : item.port.containerPort
                      ? `(${t.metaModal.containerPortHint.replace('{port}', item.port.containerPort)})`
                      : ''
                  return {
                    value: item.port.hostPort,
                    label: `${hostAddr}:${item.port.hostPort}${suffix ? ` ${suffix}` : ''}`,
                  }
                })}
              />
            </Flex>
          ) : (
            <div
              style={{
                height: 32,
                paddingInline: 12,
                borderRadius: token.borderRadius,
                border: `1px dashed ${token.colorBorder}`,
                display: 'flex',
                alignItems: 'center',
                color: token.colorTextTertiary,
                fontSize: token.fontSizeSM,
              }}
            >
              {t.metaModal.noMappedPorts}
            </div>
          ))}

        {activeDraft.accessMode === 'custom' && (
          <Input
            allowClear
            value={activeDraft.customUrl}
            placeholder={t.metaModal.customUrlPlaceholder}
            onChange={(e) => updateActiveDraft({ customUrl: e.target.value })}
            style={{ fontFamily: token.fontFamilyCode }}
          />
        )}
      </div>

      {/* 备注 */}
      <div>
        <Typography.Text
          strong
          style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 6 }}
        >
          {t.metaModal.noteLabel}
        </Typography.Text>
        <Input.TextArea
          rows={3}
          value={activeDraft.note}
          onChange={(e) => updateActiveDraft({ note: e.target.value })}
        />
      </div>

      {/* 底栏按钮 */}
      <Flex
        justify="flex-end"
        gap={8}
        style={{
          paddingTop: 12,
          borderTop: `1px solid ${token.colorSplit}`,
        }}
      >
        <Button onClick={onClose}>{t.metaModal.cancel}</Button>
        <Button type="primary" loading={submitting} onClick={handleSave}>
          {t.metaModal.save}
        </Button>
      </Flex>
    </Flex>
  )
}
