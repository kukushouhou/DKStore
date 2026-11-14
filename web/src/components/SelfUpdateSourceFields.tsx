import { useEffect, useMemo, useRef, useState } from 'react'
import { InfoCircleOutlined } from '@ant-design/icons'
import {
  AutoComplete,
  Button,
  Flex,
  Form,
  Input,
  Modal,
  Segmented,
  Select,
  Space,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import { api, showApiError, type SelfUpdateConfig } from '../api/client'
import { useI18n } from '../i18n'

const DEFAULT_PRESET_MIRRORS = [
  'https://gh-proxy.org',
  'https://gh-proxy.com',
  'https://ghfast.top',
  'https://ghproxy.net',
]

interface Props {
  layout: 'vertical' | 'horizontal'
  value: SelfUpdateConfig
  onChange: (next: SelfUpdateConfig) => void
  defaultRepo?: string
  officialMirror?: string
  presetMirrors?: string[]
  onSave?: () => void
  saving?: boolean
  safetyModalOpen: boolean
  onSafetyModalChange: (open: boolean) => void
  showInlineSafetyLink?: boolean
}

export default function SelfUpdateSourceFields({
  layout,
  value,
  onChange,
  defaultRepo = 'kukushouhou/DKStore',
  officialMirror = '',
  presetMirrors = DEFAULT_PRESET_MIRRORS,
  onSave,
  saving = false,
  safetyModalOpen,
  onSafetyModalChange,
  showInlineSafetyLink = false,
}: Props) {
  const { t } = useI18n()
  const { token } = theme.useToken()

  const [repoFocused, setRepoFocused] = useState(false)
  const [urlFocused, setUrlFocused] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testOkLatency, setTestOkLatency] = useState<number | null>(null)
  const repoBlurTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const urlBlurTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    return () => {
      if (repoBlurTimerRef.current) clearTimeout(repoBlurTimerRef.current)
      if (urlBlurTimerRef.current) clearTimeout(urlBlurTimerRef.current)
    }
  }, [])

  const onRepoFocus = () => {
    if (repoBlurTimerRef.current) clearTimeout(repoBlurTimerRef.current)
    setRepoFocused(true)
  }
  const onRepoBlur = () => {
    if (repoBlurTimerRef.current) clearTimeout(repoBlurTimerRef.current)
    repoBlurTimerRef.current = setTimeout(() => setRepoFocused(false), 180)
  }
  const onUrlFocus = () => {
    if (urlBlurTimerRef.current) clearTimeout(urlBlurTimerRef.current)
    setUrlFocused(true)
  }
  const onUrlBlur = () => {
    if (urlBlurTimerRef.current) clearTimeout(urlBlurTimerRef.current)
    urlBlurTimerRef.current = setTimeout(() => setUrlFocused(false), 180)
  }

  const isVertical = layout === 'vertical'
  const hasOfficialMirror = Boolean(officialMirror.trim())
  const cleanRepo = (value.repo || defaultRepo).trim().replace(/^\/+|\/+$/g, '') || defaultRepo
  const isOfficialRepo = cleanRepo.toLowerCase() === defaultRepo.toLowerCase()
  const effectiveChannel =
    (!hasOfficialMirror || !isOfficialRepo) && value.channel === 'mirror'
      ? 'github'
      : value.channel

  const readonlyUrl =
    effectiveChannel === 'mirror' && hasOfficialMirror
      ? `${officialMirror.replace(/\/+$/, '')}/https://github.com/${defaultRepo}`
      : `https://github.com/${cleanRepo}`

  const leadSlash = value.customHost.endsWith('/') ? '' : '/'
  const suffixOptions = useMemo(
    () => [
      {
        value: 'prefix' as const,
        label: `${leadSlash}https://github.com/${cleanRepo}`,
      },
      {
        value: 'host' as const,
        label: `${leadSlash}${cleanRepo}`,
      },
    ],
    [cleanRepo, leadSlash],
  )

  const presetOptions = useMemo(
    () => presetMirrors.map((url) => ({ value: url, label: url })),
    [presetMirrors],
  )

  const urlFocusHint =
    effectiveChannel === 'mirror'
      ? t.selfUpdate.hintMirror
      : effectiveChannel === 'custom'
        ? t.selfUpdate.hintCustom
        : t.selfUpdate.hintGitHub

  const handleRepoChange = (nextRepo: string) => {
    setTestOkLatency(null)
    const nextClean = nextRepo.trim().replace(/^\/+|\/+$/g, '') || defaultRepo
    const nextOfficial = nextClean.toLowerCase() === defaultRepo.toLowerCase()
    onChange({
      ...value,
      repo: nextRepo,
      channel: !nextOfficial && value.channel === 'mirror' ? 'github' : value.channel,
    })
  }

  const handleChannelChange = (ch: 'github' | 'mirror' | 'custom') => {
    setTestOkLatency(null)
    onChange({ ...value, channel: ch })
  }

  const handleCustomHostChange = (host: string) => {
    setTestOkLatency(null)
    onChange({ ...value, customHost: host })
  }

  const handleCustomModeChange = (mode: 'prefix' | 'host') => {
    setTestOkLatency(null)
    onChange({ ...value, customMode: mode })
  }

  const handleTest = async () => {
    setTesting(true)
    setTestOkLatency(null)
    try {
      const res = await api.testSelfUpdateSource({
        ...value,
        channel: effectiveChannel,
      })
      const ms = res.latencyMs ?? 0
      setTestOkLatency(ms)
      message.success(t.selfUpdate.testOkMsg.replace('{ms}', String(ms)))
    } catch (e) {
      showApiError(e)
    } finally {
      setTesting(false)
    }
  }

  const channelSegmented = (
    <Segmented
      block={isVertical}
      value={effectiveChannel}
      onChange={(v) => handleChannelChange(v as 'github' | 'mirror' | 'custom')}
      options={[
        {
          label: isVertical ? t.selfUpdate.optKeepGitHub : t.selfUpdate.optGitHub,
          value: 'github',
        },
        ...(hasOfficialMirror
          ? [
              {
                label: isOfficialRepo ? (
                  t.selfUpdate.optMirror
                ) : (
                  <Tooltip title={t.selfUpdate.mirrorRepoLockedTip}>
                    <span>{t.selfUpdate.optMirror}</span>
                  </Tooltip>
                ),
                value: 'mirror' as const,
                disabled: !isOfficialRepo,
              },
            ]
          : []),
        {
          label: t.selfUpdate.optCustom,
          value: 'custom',
        },
      ]}
    />
  )

  const urlInputGroup =
    effectiveChannel === 'custom' ? (
      <Space.Compact style={{ flex: 1, minWidth: 0, maxWidth: isVertical ? undefined : 580 }}>
        <AutoComplete
          value={value.customHost}
          options={presetOptions}
          onChange={handleCustomHostChange}
          onFocus={onUrlFocus}
          onBlur={onUrlBlur}
          placeholder={t.selfUpdate.customPlaceholder}
          popupMatchSelectWidth={false}
          style={{
            width: isVertical ? 190 : 200,
            flexShrink: 0,
            fontFamily: value.customHost ? token.fontFamilyCode : undefined,
          }}
        />
        <Select
          value={value.customMode}
          options={suffixOptions}
          onChange={handleCustomModeChange}
          popupMatchSelectWidth={false}
          style={{ flex: 1, minWidth: 0, fontFamily: token.fontFamilyCode }}
        />
      </Space.Compact>
    ) : (
      <Input
        readOnly
        spellCheck={false}
        value={readonlyUrl}
        onFocus={onUrlFocus}
        onBlur={onUrlBlur}
        style={{
          flex: 1,
          minWidth: 0,
          maxWidth: isVertical ? undefined : 580,
          fontFamily: token.fontFamilyCode,
          color: token.colorTextSecondary,
        }}
      />
    )

  const testBtnLabel =
    testOkLatency !== null ? `✓ ${testOkLatency}ms` : t.selfUpdate.testBtn

  const safetyModal = (
    <Modal
      open={safetyModalOpen}
      title={t.selfUpdate.safetyTitle}
      width={440}
      onCancel={() => onSafetyModalChange(false)}
      footer={
        <Button type="primary" onClick={() => onSafetyModalChange(false)}>
          {t.selfUpdate.safetyOk}
        </Button>
      }
    >
      <Space orientation="vertical" size={10}>
        <Typography.Text type="secondary">{t.selfUpdate.safetyP1}</Typography.Text>
        {hasOfficialMirror ? (
          <>
            <Typography.Text type="secondary">{t.selfUpdate.safetyP2}</Typography.Text>
            <Typography.Text type="secondary">{t.selfUpdate.safetyP3}</Typography.Text>
          </>
        ) : (
          <Typography.Text type="secondary">{t.selfUpdate.safetyP2NoMirror}</Typography.Text>
        )}
      </Space>
    </Modal>
  )

  if (isVertical) {
    return (
      <>
        <Form layout="vertical" component="div">
          <Form.Item
            label={
              <Flex align="center" justify="space-between" style={{ width: '100%' }}>
                <span>{t.selfUpdate.repoLabel}</span>
                {cleanRepo !== defaultRepo && (
                  <Button
                    type="link"
                    size="small"
                    style={{ padding: 0, height: 'auto' }}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => handleRepoChange(defaultRepo)}
                  >
                    {t.selfUpdate.repoResetBtn}
                  </Button>
                )}
              </Flex>
            }
            extra={repoFocused ? t.selfUpdate.repoFocusHint : undefined}
            style={{ marginBottom: 16 }}
          >
            <Space.Compact style={{ width: '100%' }}>
              <Space.Addon style={{ fontFamily: token.fontFamilyCode }}>github.com/</Space.Addon>
              <Input
                spellCheck={false}
                value={value.repo}
                onChange={(e) => handleRepoChange(e.target.value)}
                onFocus={onRepoFocus}
                onBlur={onRepoBlur}
                placeholder="owner/repo"
                style={{ fontFamily: token.fontFamilyCode }}
              />
            </Space.Compact>
          </Form.Item>

          <Form.Item
            label={t.selfUpdate.channelAndUrlLabel}
            extra={urlFocused ? urlFocusHint : undefined}
            style={{ marginBottom: 0 }}
          >
            <Space orientation="vertical" size={10} style={{ width: '100%' }}>
              {channelSegmented}
              <Flex gap={8} align="center">
                {urlInputGroup}
                <Button loading={testing} onClick={handleTest}>
                  {testBtnLabel}
                </Button>
              </Flex>
            </Space>
          </Form.Item>
        </Form>
        {safetyModal}
      </>
    )
  }

  // Horizontal layout (Settings Page)
  const labelWidth = 84
  return (
    <>
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        {/* 行 1：源码仓库 */}
        <div>
          <Flex align="center" gap={12} wrap>
            <Typography.Text style={{ width: labelWidth, flexShrink: 0 }}>
              {t.selfUpdate.repoLabel}
            </Typography.Text>
            <Space.Compact style={{ width: '100%', maxWidth: 580 }}>
              <Space.Addon style={{ fontFamily: token.fontFamilyCode }}>github.com/</Space.Addon>
              <Input
                spellCheck={false}
                value={value.repo}
                onChange={(e) => handleRepoChange(e.target.value)}
                onFocus={onRepoFocus}
                onBlur={onRepoBlur}
                placeholder="owner/repo"
                style={{ fontFamily: token.fontFamilyCode }}
              />
            </Space.Compact>
            {cleanRepo !== defaultRepo && (
              <Button
                type="link"
                size="small"
                style={{ padding: 0 }}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => handleRepoChange(defaultRepo)}
              >
                {t.selfUpdate.repoResetBtn}
              </Button>
            )}
          </Flex>
          {repoFocused && (
            <div style={{ paddingLeft: labelWidth + 12, marginTop: 4 }}>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {t.selfUpdate.repoFocusHint}
              </Typography.Text>
            </div>
          )}
        </div>

        {/* 行 2：更新方式 */}
        <Flex align="center" gap={12} wrap>
          <Typography.Text style={{ width: labelWidth, flexShrink: 0 }}>
            {t.selfUpdate.channelLabel}
          </Typography.Text>
          {channelSegmented}
          {showInlineSafetyLink && (
            <Button
              type="link"
              size="small"
              icon={<InfoCircleOutlined />}
              style={{ padding: 0 }}
              onClick={() => onSafetyModalChange(true)}
            >
              {t.selfUpdate.safetyLink}
            </Button>
          )}
        </Flex>

        {/* 行 3：请求地址（严格限制最大 460px 与源码仓库左/右对齐） */}
        <div>
          <Flex align="center" gap={12} wrap>
            <Typography.Text type="secondary" style={{ width: labelWidth, flexShrink: 0 }}>
              {t.selfUpdate.urlLabel}
            </Typography.Text>
            <Flex align="center" gap={8} wrap style={{ flex: 1, minWidth: 0 }}>
              {urlInputGroup}
              <Button loading={testing} onClick={handleTest}>
                {testBtnLabel}
              </Button>
              {onSave && (
                <Button type="primary" loading={saving} onClick={onSave}>
                  {t.selfUpdate.saveBtn}
                </Button>
              )}
            </Flex>
          </Flex>
          {urlFocused && (
            <div style={{ paddingLeft: labelWidth + 12, marginTop: 4 }}>
              <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                {urlFocusHint}
              </Typography.Text>
            </div>
          )}
        </div>
      </Space>
      {safetyModal}
    </>
  )
}
