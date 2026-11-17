import { useMemo, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Collapse,
  ConfigProvider,
  Descriptions,
  Drawer,
  Flex,
  Grid,
  Segmented,
  Space,
  Tag,
  Typography,
  message,
  theme,
} from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import { copyTextSafe, type AppCardItem, type ServiceItem } from '../api/client'
import { useI18n } from '../i18n'

interface DetailDrawerProps {
  open: boolean
  app: AppCardItem
  updatingNow?: boolean
  onClose: () => void
  onOpenVersionPicker: (svc: ServiceItem) => void
  onOpenComposeYaml: () => void
  onUpdateNow?: (svc: ServiceItem) => void
  onOpenDelete?: () => void
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
  updatingNow = false,
  onClose,
  onOpenVersionPicker,
  onOpenComposeYaml,
  onUpdateNow,
  onOpenDelete,
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
          <Typography.Text>
            {t.detail.ruleLockedPrefixBefore}
            <Typography.Text code>{pfx}</Typography.Text>
            {t.detail.ruleLockedPrefixAfter}
          </Typography.Text>
        )
      }
      if (igMode === 'this_version') {
        const skipped =
          targetSvc.ignoreTargetTag ||
          app.ignoreTargetTag ||
          targetSvc.targetVersion ||
          app.summaryTarget ||
          ''
        return <Typography.Text>{t.detail.ruleSkippedTag.replace('{tag}', skipped)}</Typography.Text>
      }
      if (igMode === 'forever') {
        return <Typography.Text>{t.detail.ruleMuted}</Typography.Text>
      }
    }

    const autoScope = detectAutoScopeTag(
      targetSvc.imageRef,
      targetSvc.currentVersion || app.summaryCurrent,
    )
    if (autoScope) {
      return (
        <Typography.Text>
          {t.detail.ruleAutoFromTagBefore}
          <Typography.Text code>{autoScope.cleanPrefix}</Typography.Text>
          {t.detail.ruleAutoFromTagMiddle}
          <Typography.Text code>{autoScope.rawTag}</Typography.Text>
          {t.detail.ruleAutoFromTagAfter}
        </Typography.Text>
      )
    }

    return <Typography.Text>{t.detail.ruleNormal}</Typography.Text>
  }

  const hasUpdate = svc
    ? (svc.kind === 'digest_update' || svc.kind === 'version_update') && !svc.ignored
    : false
  const curVer = svc?.currentVersion || app.summaryCurrent || t.home.unknownCurrent
  const curTime = formatDateTime(svc?.currentUpdatedAt)
  const tgtVer = svc?.targetVersion || app.summaryTarget || t.home.newImageDetected
  const tgtTime = formatDateTime(svc?.targetUpdatedAt)
  const isCaution = app.group === 'caution' || svc?.category === 'caution'
  const showPrimaryUpdateBtn = Boolean(svc && onUpdateNow && (hasUpdate || updatingNow))

  const cautionBtnTheme = useMemo(
    () =>
      isCaution
        ? {
            token: { colorPrimary: token.colorWarning },
            components: { Button: { primaryShadow: 'none' } },
          }
        : undefined,
    [isCaution, token.colorWarning],
  )

  const linkEntries = useMemo(() => {
    const lk = svc?.links ?? app.links
    if (!lk) return []
    const norm = (u?: string) =>
      (u ?? '')
        .trim()
        .toLowerCase()
        .replace(/\/+$/, '')
        .replace(/^https?:\/\/(www\.)?/, '')
    const isRegPage = (u?: string) => /(hub\.docker\.com|quay\.io)/i.test(u ?? '')
    const seen = new Set<string>()
    const items: { key: string; label: string; url: string }[] = []

    const addIfNew = (key: string, label: string, url?: string) => {
      const clean = (url ?? '').trim()
      if (!clean) return
      const n = norm(clean)
      if (!n || seen.has(n)) return
      seen.add(n)
      items.push({ key, label, url: clean })
    }

    addIfNew('releases', t.detail.linkReleases, lk.releases)

    if (
      lk.repo &&
      !isRegPage(lk.repo) &&
      norm(lk.repo) !== norm(lk.registryUrl) &&
      norm(lk.repo) !== norm(lk.website)
    ) {
      addIfNew(
        'repo',
        /github\.com/i.test(lk.repo) ? t.detail.linkGithub : t.detail.linkSourceRepo,
        lk.repo,
      )
    }

    addIfNew('website', t.detail.linkWebsite, lk.website)
    addIfNew('docs', t.detail.linkDocs, lk.docs)

    const regUrl = lk.registryUrl || (isRegPage(lk.repo) ? lk.repo : '')
    addIfNew('registry', t.detail.linkRegistry, regUrl)

    if (lk.repo && !isRegPage(lk.repo)) {
      addIfNew(
        'repo-fallback',
        /github\.com/i.test(lk.repo) ? t.detail.linkGithub : t.detail.linkSourceRepo,
        lk.repo,
      )
    }

    return items
  }, [app.links, svc?.links, t.detail])

  const cliCmd = svc
    ? app.source === 'compose' || app.source === 'dsm_project'
      ? `docker compose pull ${svc.name} && docker compose up -d --no-deps ${svc.name}`
      : `docker pull ${svc.targetRef || svc.imageRef}`
    : ''

  const descriptionItems = svc
    ? [
        {
          key: 'device',
          label: t.detail.deviceLabel,
          children: <Typography.Text>{app.hostName}</Typography.Text>,
        },
        ...(totalCount > 1 || Boolean(projectName)
          ? [
              {
                key: 'project',
                label: t.detail.projectLabel,
                children: (
                  <Typography.Text>
                    {t.detail.projectValue
                      .replace('{name}', projectName || app.name)
                      .replace('{count}', String(totalCount))}
                  </Typography.Text>
                ),
              },
            ]
          : []),
        {
          key: 'image',
          label: t.detail.currentImageLabel,
          children: (
            <Typography.Text
              style={{
                fontFamily: token.fontFamilyCode,
                fontSize: token.fontSizeSM,
                wordBreak: 'break-all',
              }}
            >
              {svc.imageRef}
            </Typography.Text>
          ),
        },
        ...(linkEntries.length > 0
          ? [
              {
                key: 'links',
                label: t.detail.linksLabel,
                children: (
                  <Space
                    size={[6, 4]}
                    wrap
                    separator={<Typography.Text type="secondary">·</Typography.Text>}
                  >
                    {linkEntries.map((item) => (
                      <Typography.Link
                        key={item.key}
                        href={item.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        style={{ fontSize: token.fontSizeSM, whiteSpace: 'nowrap' }}
                      >
                        {item.label}
                      </Typography.Link>
                    ))}
                  </Space>
                ),
              },
            ]
          : []),
        {
          key: 'rule',
          label: t.detail.ruleLabel,
          children: renderRuleContent(svc),
        },
        {
          key: 'runningVer',
          label: t.detail.runningVerLabel,
          children: (
            <Space size={6} wrap>
              <Tag style={{ margin: 0, fontFamily: token.fontFamilyCode }}>{curVer}</Tag>
              {curTime && (
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  ({curTime})
                </Typography.Text>
              )}
            </Space>
          ),
        },
        {
          key: 'availableVer',
          label: t.detail.availableVerLabel,
          children: hasUpdate ? (
            <Space size={6} wrap>
              <Tag
                color={isCaution ? 'warning' : 'processing'}
                style={{ margin: 0, fontFamily: token.fontFamilyCode, fontWeight: 600 }}
              >
                {tgtVer}
              </Tag>
              {tgtTime && (
                <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                  ({tgtTime})
                </Typography.Text>
              )}
            </Space>
          ) : svc.kind === 'uncomparable' ? (
            <Typography.Text type="secondary">{t.detail.uncomparable}</Typography.Text>
          ) : (
            <Tag color="success" variant="filled" style={{ margin: 0 }}>
              {t.detail.upToDate}
            </Tag>
          ),
        },
      ]
    : []

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
          <div style={{ minWidth: 0 }}>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {t.detail.drawerTitle.replace('{name}', svc?.name || app.name)}
            </Typography.Title>
            <Typography.Text
              type="secondary"
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
            >
              {subtitle}
            </Typography.Text>
          </div>

          {app.source !== 'vendor_managed' && (
            <Space size={8} wrap>
              {(app.composeEditable || (totalCount <= 1 && app.source === 'standalone')) && (
                <Button size="small" onClick={onOpenComposeYaml}>
                  {t.detail.updateByConfigBtn}
                </Button>
              )}
              {svc && svc.kind !== 'uncomparable' && (
                <Button
                  size="small"
                  type={showPrimaryUpdateBtn ? 'default' : 'primary'}
                  ghost={!showPrimaryUpdateBtn}
                  onClick={() => onOpenVersionPicker(svc)}
                >
                  {t.detail.specifyVersionBtn}
                </Button>
              )}
              {showPrimaryUpdateBtn && svc && onUpdateNow && (
                <ConfigProvider theme={cautionBtnTheme}>
                  <Button
                    size="small"
                    type="primary"
                    loading={updatingNow}
                    onClick={() => onUpdateNow(svc)}
                  >
                    {t.detail.updateNowBtn}
                  </Button>
                </ConfigProvider>
              )}
            </Space>
          )}
        </Flex>

        {/* 多容器卡片切换查看 */}
        {servicePool.length > 1 && (
          <Segmented
            size="small"
            value={svc?.key}
            onChange={(val) => setSelectedKey(String(val))}
            options={servicePool.map((s) => ({ label: s.name, value: s.key }))}
          />
        )}

        {/* 次要信息收纳表 */}
        {svc && (
          <>
            <Descriptions
              column={1}
              bordered
              size="small"
              styles={{ label: { width: 112, whiteSpace: 'nowrap' } }}
              items={descriptionItems}
            />

            {svc.reasonText && (
              <Alert
                type={isCaution ? 'warning' : 'info'}
                showIcon
                title={<Typography.Text style={{ fontSize: token.fontSizeSM }}>{svc.reasonText}</Typography.Text>}
              />
            )}
            {svc.hint && (
              <Alert
                type="info"
                showIcon
                title={<Typography.Text style={{ fontSize: token.fontSizeSM }}>{svc.hint}</Typography.Text>}
              />
            )}
            {svc.errorMsg && (
              <Alert
                type="error"
                showIcon
                title={<Typography.Text style={{ fontSize: token.fontSizeSM }}>{svc.errorMsg}</Typography.Text>}
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
                    <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                      {t.detail.techCollapseTitle}
                    </Typography.Text>
                  ),
                  children: (
                    <Flex vertical gap={10}>
                      {svc.repoDigest && (
                        <div>
                          <Typography.Text
                            type="secondary"
                            style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 2 }}
                          >
                            {t.detail.localDigest}
                          </Typography.Text>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {svc.repoDigest}
                          </Typography.Text>
                        </div>
                      )}
                      {svc.targetDigest && (
                        <div>
                          <Typography.Text
                            type="secondary"
                            style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 2 }}
                          >
                            {t.detail.targetDigest}
                          </Typography.Text>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {svc.targetDigest}
                          </Typography.Text>
                        </div>
                      )}
                      {(svc.targetTags?.length ?? 0) > 0 && (
                        <div>
                          <Typography.Text
                            type="secondary"
                            style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 4 }}
                          >
                            {t.detail.matchedTags}
                          </Typography.Text>
                          <Space size={[4, 4]} wrap>
                            {svc.targetTags?.map((tg) => (
                              <Tag
                                key={tg}
                                style={{ margin: 0, fontFamily: token.fontFamilyCode }}
                              >
                                {tg}
                              </Tag>
                            ))}
                          </Space>
                        </div>
                      )}
                      {cliCmd && (
                        <Card
                          size="small"
                          styles={{ body: { padding: '10px 12px' } }}
                          style={{ background: token.colorFillAlter }}
                        >
                          <Flex align="center" justify="space-between" style={{ marginBottom: 4 }}>
                            <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
                              {t.detail.howToUpdateTitle}
                            </Typography.Text>
                            <Button
                              size="small"
                              type="text"
                              icon={<CopyOutlined />}
                              onClick={async () => {
                                await copyTextSafe(cliCmd)
                                message.success(t.detail.copied)
                              }}
                            >
                              {t.detail.copyCmd}
                            </Button>
                          </Flex>
                          <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                            {cliCmd}
                          </Typography.Text>
                        </Card>
                      )}
                    </Flex>
                  ),
                },
              ]}
            />
          </>
        )}

        <Flex justify="space-between" align="center" style={{ paddingTop: 4 }}>
          <div>
            {onOpenDelete && app.source !== 'vendor_managed' && (
              <Button danger onClick={onOpenDelete}>
                {app.source === 'compose' || app.source === 'dsm_project' || totalCount > 1
                  ? t.home.deleteProjectMenu
                  : t.home.deleteContainerMenu}
              </Button>
            )}
          </div>
          <Button onClick={onClose}>{t.detail.closeBtn}</Button>
        </Flex>
      </Flex>
    </Drawer>
  )
}
