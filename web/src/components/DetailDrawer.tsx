import { useMemo, useState } from 'react'
import { Alert, Button, Collapse, Drawer, Grid, Segmented, Typography, message, theme } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import type { AppCardItem, ServiceItem } from '../api/client'
import { useI18n } from '../i18n'

interface DetailDrawerProps {
  open: boolean
  app: AppCardItem
  onClose: () => void
  onOpenVersionPicker: (svc: ServiceItem) => void
  onOpenComposeYaml: () => void
}

function formatDateTime(ts?: number): string {
  if (!ts || ts <= 0) return ''
  const d = new Date(ts * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 从镜像引用提取自动定界前缀（如 eclipse-mosquitto:2 或 mysql:8.3） */
function detectAutoScopeTag(
  imageRef: string,
  currentVer?: string,
): { rawTag: string; cleanPrefix: string } | null {
  const withoutDigest = imageRef.split('@')[0]
  const lastSlash = withoutDigest.lastIndexOf('/')
  const colonIdx = withoutDigest.indexOf(':', lastSlash + 1)
  if (colonIdx < 0) return null
  const rawTag = withoutDigest.slice(colonIdx + 1)
  const cleanPrefix = rawTag.replace(/^v/i, '').split(/[-+_]/)[0]
  if (!/^\d+(\.\d+)?$/.test(cleanPrefix)) return null

  const tagSegCount = cleanPrefix.split('.').length
  if (tagSegCount === 1) {
    return { rawTag, cleanPrefix }
  }
  if (currentVer) {
    const cleanCur = currentVer.replace(/^v/i, '').split(/[-+_]/)[0]
    if (cleanCur.startsWith(`${cleanPrefix}.`) && cleanCur.split('.').length > tagSegCount) {
      return { rawTag, cleanPrefix }
    }
  }
  return null
}

export default function DetailDrawer({
  open,
  app,
  onClose,
  onOpenVersionPicker,
  onOpenComposeYaml,
}: DetailDrawerProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const screens = Grid.useBreakpoint()

  const servicePool = useMemo(() => {
    if (app.services.length > 0) return app.services
    return app.allServices ?? []
  }, [app.allServices, app.services])

  const [selectedKey, setSelectedKey] = useState<string>('')

  const svc: ServiceItem | undefined =
    servicePool.find((s) => s.key === selectedKey) ?? servicePool[0]

  const totalCount = app.totalServiceCount ?? app.allServices?.length ?? app.services.length
  const projectName = app.projectName || (totalCount > 1 ? app.appName || app.name : '')
  const subtitle =
    projectName && projectName !== app.name
      ? `${app.hostName} · ${t.home.projectSubPrefix}${projectName}`
      : app.hostName

  const renderRuleContent = (targetSvc: ServiceItem) => {
    const igMode = targetSvc.ignoreMode || app.ignoreMode
    const isIg = targetSvc.ignored || app.ignored || Boolean(igMode)

    if (isIg) {
      if (igMode === 'prefix') {
        const pfx = targetSvc.ignorePrefix || app.ignorePrefix || ''
        return (
          <span>
            {t.detail.ruleLockedPrefixBefore}
            <code style={{ fontFamily: 'monospace', fontWeight: 600 }}>{pfx}</code>
            {t.detail.ruleLockedPrefixAfter}
          </span>
        )
      }
      if (igMode === 'this_version') {
        const skipped =
          targetSvc.ignoreTargetTag ||
          app.ignoreTargetTag ||
          targetSvc.targetVersion ||
          app.summaryTarget ||
          ''
        return <span>{t.detail.ruleSkippedTag.replace('{tag}', skipped)}</span>
      }
      if (igMode === 'forever') {
        return <span>{t.detail.ruleMuted}</span>
      }
    }

    const autoScope = detectAutoScopeTag(
      targetSvc.imageRef,
      targetSvc.currentVersion || app.summaryCurrent,
    )
    if (autoScope) {
      return (
        <span>
          {t.detail.ruleAutoFromTagBefore}
          <code style={{ fontFamily: 'monospace', fontWeight: 600 }}>{autoScope.cleanPrefix}</code>
          {t.detail.ruleAutoFromTagMiddle}
          {autoScope.rawTag}
          {t.detail.ruleAutoFromTagAfter}
        </span>
      )
    }

    return <span>{t.detail.ruleNormal}</span>
  }

  const rowStyle: React.CSSProperties = {
    padding: '10px 14px',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: 12,
    fontSize: 12,
    borderBottom: `1px solid ${token.colorBorderSecondary}`,
  }

  const hasUpdate = svc
    ? (svc.kind === 'digest_update' || svc.kind === 'version_update') && !svc.ignored
    : false
  const curVer = svc?.currentVersion || app.summaryCurrent || t.home.unknownCurrent
  const curTime = formatDateTime(svc?.currentUpdatedAt)
  const tgtVer = svc?.targetVersion || app.summaryTarget || t.home.newImageDetected
  const tgtTime = formatDateTime(svc?.targetUpdatedAt)
  const isCaution = app.group === 'caution' || svc?.category === 'caution'

  const cliCmd = svc
    ? app.source === 'compose' || app.source === 'dsm_project'
      ? `docker compose pull ${svc.name} && docker compose up -d --no-deps ${svc.name}`
      : `docker pull ${svc.targetRef || svc.imageRef}`
    : ''

  return (
    <Drawer
      open={open}
      onClose={onClose}
      placement={screens.md ? 'right' : 'bottom'}
      width={screens.md ? 520 : undefined}
      height={screens.md ? undefined : '82vh'}
      closable={false}
      title={null}
      styles={{ body: { padding: 20 } }}
    >
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
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 16, fontWeight: 700, color: token.colorText }}>
              {t.detail.drawerTitle.replace('{name}', svc?.name || app.name)}
            </div>
            <div style={{ fontSize: 12, color: token.colorTextSecondary, marginTop: 2 }}>
              {subtitle}
            </div>
          </div>

          {app.source !== 'vendor_managed' && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              {(app.composeEditable || (totalCount <= 1 && app.source === 'standalone')) && (
                <Button size="small" onClick={onOpenComposeYaml}>
                  {t.detail.updateByConfigBtn}
                </Button>
              )}
              {svc && (
                <Button
                  size="small"
                  type="primary"
                  ghost
                  onClick={() => onOpenVersionPicker(svc)}
                >
                  {t.detail.specifyVersionBtn}
                </Button>
              )}
            </div>
          )}
        </div>

        {/* 多容器卡片切换查看 */}
        {servicePool.length > 1 && (
          <Segmented
            size="small"
            value={svc?.key}
            onChange={(val) => setSelectedKey(String(val))}
            options={servicePool.map((s) => ({ label: s.name, value: s.key }))}
          />
        )}

        {/* 次要信息收纳表（对齐设计稿第 6 页） */}
        {svc && (
          <>
            <div
              style={{
                borderRadius: 12,
                background: token.colorFillQuaternary,
                border: `1px solid ${token.colorBorderSecondary}`,
                overflow: 'hidden',
              }}
            >
              <div style={rowStyle}>
                <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                  {t.detail.deviceLabel}
                </span>
                <span style={{ color: token.colorText, textAlign: 'right' }}>{app.hostName}</span>
              </div>

              {(totalCount > 1 || Boolean(projectName)) && (
                <div style={rowStyle}>
                  <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                    {t.detail.projectLabel}
                  </span>
                  <span style={{ color: token.colorText, textAlign: 'right' }}>
                    {t.detail.projectValue
                      .replace('{name}', projectName || app.name)
                      .replace('{count}', String(totalCount))}
                  </span>
                </div>
              )}

              <div style={rowStyle}>
                <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                  {t.detail.currentImageLabel}
                </span>
                <span
                  style={{
                    fontFamily: 'monospace',
                    color: token.colorText,
                    textAlign: 'right',
                    wordBreak: 'break-all',
                  }}
                >
                  {svc.imageRef}
                </span>
              </div>

              <div style={rowStyle}>
                <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                  {t.detail.ruleLabel}
                </span>
                <span style={{ color: token.colorText, textAlign: 'right' }}>
                  {renderRuleContent(svc)}
                </span>
              </div>

              <div style={rowStyle}>
                <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                  {t.detail.runningVerLabel}
                </span>
                <span
                  style={{
                    fontFamily: 'monospace',
                    color: token.colorText,
                    textAlign: 'right',
                  }}
                >
                  {curVer}
                  {curTime ? ` (${curTime})` : ''}
                </span>
              </div>

              <div style={{ ...rowStyle, borderBottom: 'none' }}>
                <span style={{ color: token.colorTextSecondary, flexShrink: 0 }}>
                  {t.detail.availableVerLabel}
                </span>
                {hasUpdate ? (
                  <span
                    style={{
                      fontFamily: 'monospace',
                      fontWeight: 600,
                      color: isCaution ? '#d97706' : token.colorPrimary,
                      textAlign: 'right',
                    }}
                  >
                    {tgtVer}
                    {tgtTime ? ` (${tgtTime})` : ''}
                  </span>
                ) : svc.kind === 'uncomparable' ? (
                  <span style={{ color: token.colorTextSecondary }}>{t.detail.uncomparable}</span>
                ) : (
                  <span style={{ color: '#10b981', fontWeight: 500 }}>{t.detail.upToDate}</span>
                )}
              </div>
            </div>

            {svc.reasonText && (
              <Alert
                type={isCaution ? 'warning' : 'info'}
                showIcon
                title={<span style={{ fontSize: 12 }}>{svc.reasonText}</span>}
              />
            )}
            {svc.hint && (
              <Alert type="info" showIcon title={<span style={{ fontSize: 12 }}>{svc.hint}</span>} />
            )}
            {svc.errorMsg && (
              <Alert
                type="error"
                showIcon
                title={<span style={{ fontSize: 12 }}>{svc.errorMsg}</span>}
              />
            )}

            {/* 折叠收纳的技术摘要与命令行参考 */}
            <Collapse
              ghost
              size="small"
              items={[
                {
                  key: 'tech',
                  label: (
                    <span style={{ fontSize: 12, color: token.colorTextSecondary }}>
                      {t.detail.techCollapseTitle}
                    </span>
                  ),
                  children: (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 12 }}>
                      {svc.repoDigest && (
                        <div>
                          <div style={{ color: token.colorTextSecondary, marginBottom: 2 }}>
                            {t.detail.localDigest}
                          </div>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {svc.repoDigest}
                          </Typography.Text>
                        </div>
                      )}
                      {svc.targetDigest && (
                        <div>
                          <div style={{ color: token.colorTextSecondary, marginBottom: 2 }}>
                            {t.detail.targetDigest}
                          </div>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {svc.targetDigest}
                          </Typography.Text>
                        </div>
                      )}
                      {(svc.targetTags?.length ?? 0) > 0 && (
                        <div>
                          <div style={{ color: token.colorTextSecondary, marginBottom: 2 }}>
                            {t.detail.matchedTags}
                          </div>
                          <Typography.Text style={{ fontFamily: 'monospace', fontSize: 12 }}>
                            {svc.targetTags?.join(', ')}
                          </Typography.Text>
                        </div>
                      )}
                      {cliCmd && (
                        <div
                          style={{
                            borderRadius: 10,
                            padding: '10px 12px',
                            background: token.colorFillQuaternary,
                            border: `1px solid ${token.colorBorderSecondary}`,
                          }}
                        >
                          <div
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              marginBottom: 4,
                            }}
                          >
                            <span style={{ fontWeight: 500 }}>{t.detail.howToUpdateTitle}</span>
                            <Button
                              size="small"
                              type="text"
                              icon={<CopyOutlined />}
                              onClick={() => {
                                navigator.clipboard.writeText(cliCmd)
                                message.success(t.detail.copied)
                              }}
                            >
                              {t.detail.copyCmd}
                            </Button>
                          </div>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {cliCmd}
                          </Typography.Text>
                        </div>
                      )}
                    </div>
                  ),
                },
              ]}
            />
          </>
        )}

        <div style={{ display: 'flex', justifyContent: 'flex-end', paddingTop: 4 }}>
          <Button onClick={onClose}>{t.detail.closeBtn}</Button>
        </div>
      </div>
    </Drawer>
  )
}
