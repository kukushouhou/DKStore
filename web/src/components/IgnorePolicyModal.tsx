import { useMemo, useState } from 'react'
import { Button, Modal, message, theme } from 'antd'
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
    const active = mode === itemMode
    return (
      <div
        key={itemMode}
        className="dkstore-radio-card"
        onClick={() => setMode(itemMode)}
        style={{
          padding: '10px 12px',
          borderRadius: 12,
          border: active
            ? `1px solid ${token.colorPrimary}`
            : `1px solid ${token.colorBorderSecondary}`,
          background: active ? 'rgba(47, 107, 255, 0.08)' : token.colorFillQuaternary,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
          <input
            type="radio"
            checked={active}
            onChange={() => setMode(itemMode)}
            style={{ marginTop: 3, accentColor: token.colorPrimary, cursor: 'pointer' }}
          />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div
              style={{
                fontSize: 13,
                fontWeight: 500,
                color: active ? token.colorPrimary : token.colorText,
              }}
            >
              {titleText}
            </div>
            {extraNode}
            {descText && (
              <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 2 }}>
                {descText}
              </div>
            )}
          </div>
        </div>
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div
          style={{
            paddingBottom: 12,
            borderBottom: `1px solid ${token.colorBorderSecondary}`,
          }}
        >
          <div style={{ fontSize: 16, fontWeight: 700, color: token.colorText }}>
            {t.ignoreModal.title.replace('{name}', name)}
          </div>
          <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 4 }}>
            {subtitle}
            {currentVersion && (
              <>
                {' · '}
                {t.ignoreModal.currentLabel}{' '}
                <code style={{ fontFamily: 'monospace', color: token.colorText }}>
                  {currentVersion}
                </code>
              </>
            )}
            {targetVersion && (
              <>
                {' → '}
                {t.ignoreModal.foundNewLabel}{' '}
                <code style={{ fontFamily: 'monospace', color: token.colorPrimary }}>
                  {targetVersion}
                </code>
              </>
            )}
          </div>
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
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
        </div>

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
            {t.ignoreModal.footerNote}
          </span>
          <div style={{ display: 'flex', gap: 8 }}>
            <Button onClick={onClose}>{t.ignoreModal.cancel}</Button>
            <Button type="primary" loading={submitting} onClick={handleSubmit}>
              {t.ignoreModal.confirm}
            </Button>
          </div>
        </div>
      </div>
  )
}
