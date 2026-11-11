import { useMemo, useState } from 'react'
import { Button, Flex, Modal, Radio, Space, Typography, message, theme } from 'antd'
import { api, type IgnoreMode } from '../api/client'
import { useI18n } from '../i18n'
import LockableVersionBar from './LockableVersionBar'
import { parseNumericSegs } from './versionUtils'

interface IgnorePolicyModalProps {
  open: boolean
  name: string
  subtitle: string
  serviceKeys: string[]
  currentVersion?: string
  targetVersion?: string
  targetDigest?: string
  onClose: () => void
  onSuccess: () => void
}

export default function IgnorePolicyModal(props: IgnorePolicyModalProps) {
  return (
    <Modal
      open={props.open}
      onCancel={props.onClose}
      footer={null}
      width={480}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {props.open && <IgnorePolicyModalBody {...props} />}
    </Modal>
  )
}

function IgnorePolicyModalBody({
  name,
  subtitle,
  serviceKeys,
  currentVersion,
  targetVersion,
  targetDigest,
  onClose,
  onSuccess,
}: IgnorePolicyModalProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()

  const [mode, setMode] = useState<IgnoreMode>('this_version')
  const [lockDepth, setLockDepth] = useState(1)
  const [submitting, setSubmitting] = useState(false)

  const numericSegs = useMemo(() => parseNumericSegs(currentVersion), [currentVersion])
  const safeDepth = Math.min(Math.max(1, lockDepth), Math.max(1, numericSegs.length - 1))
  const lockedPrefix = numericSegs.length >= 2 ? numericSegs.slice(0, safeDepth).join('.') : ''

  const cleanTargetTag =
    targetVersion && !targetVersion.includes(' ') ? targetVersion : undefined

  const handleSubmit = async () => {
    if (serviceKeys.length === 0) return
    setSubmitting(true)
    try {
      await api.setIgnore({
        serviceKeys,
        mode,
        prefix: mode === 'prefix' ? lockedPrefix : undefined,
        targetTag: mode === 'this_version' ? cleanTargetTag : undefined,
        targetDigest: mode === 'this_version' ? targetDigest : undefined,
      })
      message.success(t.ignoreModal.success)
      onSuccess()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const renderCard = (
    itemMode: IgnoreMode,
    titleText: string,
    descText?: string,
    extraNode?: React.ReactNode,
  ) => {
    const checked = mode === itemMode
    return (
      <div
        key={itemMode}
        onClick={() => setMode(itemMode)}
        style={{
          padding: '12px 14px',
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
            onChange={() => setMode(itemMode)}
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
            {descText && (
              <Typography.Text
                type="secondary"
                style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
              >
                {descText}
              </Typography.Text>
            )}
          </div>
        </Flex>
      </div>
    )
  }

  return (
    <Flex vertical gap={16}>
      <div
        style={{
          paddingBottom: 12,
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <Typography.Title level={5} style={{ margin: 0 }}>
          {t.ignoreModal.title.replace('{name}', name)}
        </Typography.Title>
        <Typography.Text
          type="secondary"
          style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
        >
          {subtitle}
          {currentVersion && (
            <>
              {' · '}
              {t.ignoreModal.currentLabel} <Typography.Text code>{currentVersion}</Typography.Text>
            </>
          )}
          {targetVersion && (
            <>
              {' → '}
              {t.ignoreModal.foundNewLabel}{' '}
              <Typography.Text
                code
                style={{ color: token.colorPrimary, borderColor: token.colorPrimaryBorder }}
              >
                {targetVersion}
              </Typography.Text>
            </>
          )}
        </Typography.Text>
      </div>

      <Flex vertical gap={8}>
        {/* 1. 默认优先选中：仅跳过本次新版本 */}
        {renderCard(
          'this_version',
          cleanTargetTag
            ? t.ignoreModal.skipThisVerTitle.replace('{ver}', cleanTargetTag)
            : t.ignoreModal.skipThisVerGenericTitle,
          t.ignoreModal.skipThisVerDesc,
        )}

        {/* 2. 当前版本含 2 段及以上数字时：拆解点击加锁 */}
        {numericSegs.length >= 2 &&
          renderCard(
            'prefix',
            t.ignoreModal.lockPrefixTitle,
            undefined,
            <LockableVersionBar
              segments={numericSegs}
              depth={safeDepth}
              active={mode === 'prefix'}
              onDepthChange={(d) => {
                setMode('prefix')
                setLockDepth(d)
              }}
            />,
          )}

        {/* 3. 不再提醒更新 */}
        {renderCard('forever', t.ignoreModal.muteForeverTitle, t.ignoreModal.muteForeverDesc)}
      </Flex>

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
          {t.ignoreModal.footerNote}
        </Typography.Text>
        <Space size={8}>
          <Button onClick={onClose}>{t.ignoreModal.cancel}</Button>
          <Button type="primary" loading={submitting} onClick={handleSubmit}>
            {t.ignoreModal.confirm}
          </Button>
        </Space>
      </Flex>
    </Flex>
  )
}
