import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Button,
  Checkbox,
  Col,
  Flex,
  Input,
  Modal,
  Row,
  Spin,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import {
  ArrowUpOutlined,
  ExclamationCircleOutlined,
  FolderOutlined,
  HddOutlined,
  PlusOutlined,
  RightOutlined,
} from '@ant-design/icons'
import { api, type SuggestedComposeDir } from '../api/client'
import { useI18n } from '../i18n'

const SAFE_DIR_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]*$/

export type ComposeSaveMode = 'orig' | 'new'

interface ComposeSaveLocationBarProps {
  appKey: string
  hostName?: string
  projectName?: string
  composePath?: string
  pathMissing?: boolean
  pathReadOnly?: boolean
  origWritable?: boolean
  suggestedDirs?: SuggestedComposeDir[]
  saveMode: ComposeSaveMode
  pickedNewFile: string
  onChangeSaveMode: (mode: ComposeSaveMode) => void
  onSelectNewFile: (fullFilePath: string) => void
}

function normalizePosixDirClient(raw?: string): string {
  const s = (raw || '').trim().replace(/\\/g, '/').replace(/\/+$/g, '')
  if (!s) return '/'
  return s.startsWith('/') ? s : `/${s}`
}

function parentPosixDir(dir: string): string {
  const clean = normalizePosixDirClient(dir)
  if (clean === '/') return '/'
  const idx = clean.lastIndexOf('/')
  return idx <= 0 ? '/' : clean.slice(0, idx)
}

export default function ComposeSaveLocationBar({
  appKey,
  hostName,
  projectName,
  composePath,
  pathMissing,
  pathReadOnly,
  origWritable = false,
  suggestedDirs = [],
  saveMode,
  pickedNewFile,
  onChangeSaveMode,
  onSelectNewFile,
}: ComposeSaveLocationBarProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const [pickerOpen, setPickerOpen] = useState(false)

  const canSaveOrig = Boolean(origWritable && composePath)

  const radioCircle = (checked: boolean, disabled = false) => (
    <span
      style={{
        width: 14,
        height: 14,
        borderRadius: '50%',
        border: `1.5px solid ${
          disabled
            ? token.colorBorder
            : checked
              ? token.colorPrimary
              : token.colorTextQuaternary
        }`,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
      }}
    >
      {checked && !disabled && (
        <span
          style={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            background: token.colorPrimary,
          }}
        />
      )}
    </span>
  )

  return (
    <>
      <div
        style={{
          padding: '10px 12px',
          borderRadius: token.borderRadiusLG,
          border: `1px solid ${token.colorWarningBorder}`,
          background: token.colorFillAlter,
        }}
      >
        <Flex vertical gap={10}>
          <Flex align="center" gap={8}>
            <ExclamationCircleOutlined
              style={{ color: token.colorWarning, fontSize: 14, flexShrink: 0 }}
            />
            <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
              {pathMissing ? (
                composePath ? (
                  <>
                    {t.composeSaveLocation.missingWithPathBefore}
                    <Typography.Text code style={{ fontWeight: 400 }}>
                      {composePath}
                    </Typography.Text>
                    {t.composeSaveLocation.missingWithPathAfter}
                  </>
                ) : (
                  t.composeSaveLocation.missingNoPath
                )
              ) : (
                <>
                  {t.composeSaveLocation.readOnlyWithPathBefore}
                  {composePath && (
                    <Typography.Text code style={{ fontWeight: 400 }}>
                      {composePath}
                    </Typography.Text>
                  )}
                  {t.composeSaveLocation.readOnlyWithPathAfter}
                </>
              )}
            </Typography.Text>
          </Flex>

          <Row gutter={[8, 8]}>
            {composePath && (
              <Col xs={24} md={12}>
                <div
                  role="button"
                  tabIndex={canSaveOrig ? 0 : -1}
                  onClick={() => {
                    if (canSaveOrig) onChangeSaveMode('orig')
                  }}
                  onKeyDown={(e) => {
                    if (canSaveOrig && (e.key === 'Enter' || e.key === ' ')) {
                      e.preventDefault()
                      onChangeSaveMode('orig')
                    }
                  }}
                  style={{
                    height: '100%',
                    padding: '8px 12px',
                    borderRadius: token.borderRadius,
                    border: `1px solid ${
                      canSaveOrig && saveMode === 'orig'
                        ? token.colorPrimary
                        : token.colorBorderSecondary
                    }`,
                    background: token.colorBgContainer,
                    opacity: canSaveOrig ? 1 : 0.55,
                    cursor: canSaveOrig ? 'pointer' : 'not-allowed',
                    transition: 'border-color 0.15s ease',
                  }}
                >
                  <Flex align="center" gap={6} wrap="wrap">
                    {radioCircle(saveMode === 'orig', !canSaveOrig)}
                    <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
                      {t.composeSaveLocation.saveToOrig}
                    </Typography.Text>
                    {(pathReadOnly || !canSaveOrig) && (
                      <Tag color="error" variant="filled" style={{ margin: 0, fontSize: 11 }}>
                        {t.composeSaveLocation.noWritePermBadge}
                      </Tag>
                    )}
                  </Flex>
                  <Typography.Text
                    type="secondary"
                    ellipsis={{ tooltip: composePath }}
                    style={{
                      display: 'block',
                      paddingLeft: 20,
                      marginTop: 2,
                      fontSize: 12,
                      fontFamily: token.fontFamilyCode,
                    }}
                  >
                    {composePath}
                  </Typography.Text>
                </div>
              </Col>
            )}

            <Col xs={24} md={composePath ? 12 : 24}>
              <div
                role="button"
                tabIndex={0}
                onClick={() => {
                  if (pickedNewFile) {
                    onChangeSaveMode('new')
                  } else {
                    setPickerOpen(true)
                  }
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    if (pickedNewFile) onChangeSaveMode('new')
                    else setPickerOpen(true)
                  }
                }}
                style={{
                  height: '100%',
                  padding: '8px 12px',
                  borderRadius: token.borderRadius,
                  border: `1px solid ${
                    saveMode === 'new' ? token.colorPrimary : token.colorBorderSecondary
                  }`,
                  background: token.colorBgContainer,
                  cursor: 'pointer',
                  transition: 'border-color 0.15s ease',
                }}
              >
                <Flex align="center" justify="space-between" gap={8}>
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <Flex align="center" gap={6}>
                      {radioCircle(saveMode === 'new')}
                      <Typography.Text strong style={{ fontSize: token.fontSizeSM }}>
                        {t.composeSaveLocation.saveToNew}
                      </Typography.Text>
                    </Flex>
                    <Typography.Text
                      ellipsis={{ tooltip: pickedNewFile || undefined }}
                      style={{
                        display: 'block',
                        paddingLeft: 20,
                        marginTop: 2,
                        fontSize: 12,
                        fontFamily: pickedNewFile ? token.fontFamilyCode : undefined,
                        color:
                          pickedNewFile && saveMode === 'new'
                            ? token.colorPrimary
                            : token.colorTextSecondary,
                      }}
                    >
                      {pickedNewFile || t.composeSaveLocation.notPickedYet}
                    </Typography.Text>
                  </div>

                  <Button
                    size="small"
                    type="primary"
                    ghost
                    onClick={(e) => {
                      e.stopPropagation()
                      setPickerOpen(true)
                    }}
                  >
                    {t.composeSaveLocation.pickFolderBtn}
                  </Button>
                </Flex>
              </div>
            </Col>
          </Row>
        </Flex>
      </div>

      <HostFolderPickerModal
        open={pickerOpen}
        appKey={appKey}
        hostName={hostName}
        projectName={projectName}
        suggestedDirs={suggestedDirs}
        initialFile={pickedNewFile}
        onClose={() => setPickerOpen(false)}
        onConfirmFile={(filePath) => {
          onSelectNewFile(filePath)
          onChangeSaveMode('new')
          setPickerOpen(false)
        }}
      />
    </>
  )
}

interface HostFolderPickerModalProps {
  open: boolean
  appKey: string
  hostName?: string
  projectName?: string
  suggestedDirs: SuggestedComposeDir[]
  initialFile?: string
  onClose: () => void
  onConfirmFile: (fullFilePath: string) => void
}

function HostFolderPickerModal({
  open,
  appKey,
  hostName,
  projectName,
  suggestedDirs,
  initialFile,
  onClose,
  onConfirmFile,
}: HostFolderPickerModalProps) {
  return (
    <Modal
      open={open}
      onCancel={onClose}
      footer={null}
      width={560}
      style={{ maxWidth: '94vw' }}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { padding: 0 } }}
    >
      {open && (
        <HostFolderPickerBody
          appKey={appKey}
          hostName={hostName}
          projectName={projectName}
          suggestedDirs={suggestedDirs}
          initialFile={initialFile}
          onClose={onClose}
          onConfirmFile={onConfirmFile}
        />
      )}
    </Modal>
  )
}

function HostFolderPickerBody({
  appKey,
  hostName,
  projectName,
  suggestedDirs,
  initialFile,
  onClose,
  onConfirmFile,
}: Omit<HostFolderPickerModalProps, 'open'>) {
  const { token } = theme.useToken()
  const { t } = useI18n()

  const safeProjFolder = useMemo(() => {
    const clean = (projectName || '').trim()
    return SAFE_DIR_RE.test(clean) ? clean : ''
  }, [projectName])

  const startDir = useMemo(() => {
    if (initialFile) {
      return parentPosixDir(initialFile)
    }
    const firstData = suggestedDirs.find((d) => d.kind === 'data_parent' && d.path)
    if (firstData) return normalizePosixDirClient(firstData.path)
    if (suggestedDirs[0]?.path) return normalizePosixDirClient(suggestedDirs[0].path)
    return '/'
  }, [initialFile, suggestedDirs])

  const [curDir, setCurDir] = useState<string>(startDir)
  const [dirs, setDirs] = useState<string[]>([])
  const [curWritable, setCurWritable] = useState<boolean>(true)
  const [homeDir, setHomeDir] = useState<string>('')
  const [loading, setLoading] = useState<boolean>(true)

  const [mkdirOpen, setMkdirOpen] = useState<boolean>(false)
  const [mkdirName, setMkdirName] = useState<string>('')
  const [mkdirSubmitting, setMkdirSubmitting] = useState<boolean>(false)

  // 默认不勾选自动按项目名新建子文件夹
  const [useProjSubfolder, setUseProjSubfolder] = useState<boolean>(false)
  const [checking, setChecking] = useState<boolean>(false)
  const [errorNotice, setErrorNotice] = useState<string>('')
  const [overwritePrompt, setOverwritePrompt] = useState<{
    targetFile: string
    kind: 'same' | 'other_stopped' | 'other_untracked'
    owner?: string
  } | null>(null)

  const loadDir = useCallback(
    async function fetchDir(targetPath: string, fallbackToRoot = false): Promise<void> {
      setLoading(true)
      setErrorNotice('')
      setOverwritePrompt(null)
      try {
        const res = await api.listAppComposeDirs(appKey, targetPath)
        setCurDir(normalizePosixDirClient(res.path))
        setDirs(res.dirs ?? [])
        setCurWritable(res.writable)
        if (res.homeDir) {
          setHomeDir(normalizePosixDirClient(res.homeDir))
        }
      } catch (e) {
        if (fallbackToRoot && targetPath !== '/') {
          await fetchDir('/', false)
          return
        }
        setErrorNotice((e as Error).message)
      } finally {
        setLoading(false)
      }
    },
    [appKey],
  )

  useEffect(() => {
    void loadDir(startDir, true)
  }, [loadDir, startDir])

  const navigateTo = (nextPath: string) => {
    const norm = normalizePosixDirClient(nextPath)
    setMkdirOpen(false)
    setMkdirName('')
    setUseProjSubfolder(false)
    setErrorNotice('')
    setOverwritePrompt(null)
    void loadDir(norm, false)
  }

  const quickJumpItems = useMemo(() => {
    const out: SuggestedComposeDir[] = []
    const seen = new Set<string>()
    for (const item of suggestedDirs) {
      const p = normalizePosixDirClient(item.path)
      if (p && p !== '/' && !seen.has(p)) {
        seen.add(p)
        out.push({ kind: item.kind, path: p })
      }
    }
    if (homeDir && homeDir !== '/' && !seen.has(homeDir)) {
      seen.add(homeDir)
      out.push({ kind: 'home', path: homeDir })
    }
    return out
  }, [homeDir, suggestedDirs])

  const breadcrumbs = useMemo(() => {
    if (curDir === '/') return []
    return curDir.split('/').filter(Boolean)
  }, [curDir])

  const curBaseName = breadcrumbs[breadcrumbs.length - 1] || ''
  const showProjSubCheckbox = Boolean(safeProjFolder && curBaseName !== safeProjFolder)
  const effectiveSubFolder = showProjSubCheckbox && useProjSubfolder ? safeProjFolder : ''

  const previewTargetFile = useMemo(() => {
    const base = curDir === '/' ? '' : curDir
    const sub = effectiveSubFolder ? `/${effectiveSubFolder}` : ''
    return `${base}${sub}/compose.yaml`
  }, [curDir, effectiveSubFolder])

  const handleCreateFolder = async () => {
    const raw = mkdirName.trim()
    if (!raw) {
      setErrorNotice(t.composeSaveLocation.errEmptyFolderName)
      return
    }
    if (!SAFE_DIR_RE.test(raw)) {
      setErrorNotice(t.composeSaveLocation.errInvalidFolderName)
      return
    }
    if (!curWritable) {
      setErrorNotice(t.composeSaveLocation.errMkdirNotWritable)
      return
    }
    setMkdirSubmitting(true)
    setErrorNotice('')
    try {
      const res = await api.createAppComposeDir(appKey, curDir, raw)
      setMkdirOpen(false)
      setMkdirName('')
      setUseProjSubfolder(false)
      await loadDir(res.path || `${curDir === '/' ? '' : curDir}/${raw}`, false)
    } catch (e) {
      setErrorNotice((e as Error).message)
    } finally {
      setMkdirSubmitting(false)
    }
  }

  const handleConfirm = async () => {
    setChecking(true)
    setErrorNotice('')
    setOverwritePrompt(null)
    try {
      const res = await api.checkAppComposeDir(appKey, curDir, effectiveSubFolder)
      if (!res.writable) {
        setErrorNotice(t.composeSaveLocation.errDirNotWritable)
        return
      }
      if (res.existKind === 'other_running') {
        setErrorNotice(
          t.composeSaveLocation.errInUseRunning.replace('{owner}', res.owner || ''),
        )
        return
      }
      if (
        res.existKind === 'same' ||
        res.existKind === 'other_stopped' ||
        res.existKind === 'other_untracked'
      ) {
        setOverwritePrompt({
          targetFile: res.targetFile,
          kind: res.existKind,
          owner: res.owner,
        })
        return
      }
      onConfirmFile(res.targetFile)
    } catch (e) {
      setErrorNotice((e as Error).message)
    } finally {
      setChecking(false)
    }
  }

  return (
    <div>
      {/* 标题栏 */}
      <Flex
        align="center"
        justify="space-between"
        style={{
          padding: '12px 16px',
          borderBottom: `1px solid ${token.colorSplit}`,
        }}
      >
        <Flex align="center" gap={8}>
          <Typography.Text strong style={{ fontSize: 14 }}>
            {t.composeSaveLocation.modalTitle}
          </Typography.Text>
          {hostName && (
            <Tag variant="filled" style={{ margin: 0 }}>
              {hostName}
            </Tag>
          )}
        </Flex>
        <Button type="text" size="small" onClick={onClose}>
          ✕
        </Button>
      </Flex>

      {/* 主体区 */}
      <Flex vertical gap={10} style={{ padding: '12px 16px' }}>
        {/* 快捷跳转 */}
        {quickJumpItems.length > 0 && (
          <Flex wrap="wrap" align="center" gap={6}>
            <Typography.Text type="secondary" style={{ fontSize: 12, marginRight: 2 }}>
              {t.composeSaveLocation.quickJumpLabel}
            </Typography.Text>
            {quickJumpItems.map((item) => (
              <Button
                key={item.path}
                size="small"
                icon={
                  <FolderOutlined
                    style={{
                      color:
                        item.kind === 'data_parent'
                          ? token.colorPrimary
                          : token.colorTextSecondary,
                    }}
                  />
                }
                onClick={() => navigateTo(item.path)}
                style={{ fontSize: 12 }}
              >
                {item.kind === 'data_parent' ? (
                  <>
                    <span>{t.composeSaveLocation.dataDirPrefix}</span>
                    <span
                      style={{
                        marginLeft: 4,
                        fontFamily: token.fontFamilyCode,
                        color: token.colorTextSecondary,
                      }}
                    >
                      {item.path}
                    </span>
                  </>
                ) : (
                  <span style={{ fontFamily: token.fontFamilyCode }}>{item.path}</span>
                )}
              </Button>
            ))}
          </Flex>
        )}

        {/* 路径工具栏：返回上级 + 根目录 › opt › docker 面包屑 + 新建文件夹 */}
        <Flex
          align="center"
          justify="space-between"
          gap={8}
          style={{
            padding: '5px 8px',
            borderRadius: token.borderRadius,
            border: `1px solid ${token.colorBorderSecondary}`,
            background: token.colorFillAlter,
          }}
        >
          <Flex align="center" gap={6} wrap="wrap" style={{ minWidth: 0, flex: 1 }}>
            <Tooltip title={t.composeSaveLocation.upDirTip}>
              <Button
                type="text"
                size="small"
                disabled={curDir === '/'}
                icon={<ArrowUpOutlined />}
                aria-label={t.composeSaveLocation.upDirTip}
                onClick={() => navigateTo(parentPosixDir(curDir))}
              />
            </Tooltip>
            <span
              style={{
                width: 1,
                height: 14,
                background: token.colorBorderSecondary,
                flexShrink: 0,
              }}
            />

            <Button
              type="text"
              size="small"
              icon={<HddOutlined />}
              onClick={() => navigateTo('/')}
              style={{
                paddingInline: 6,
                fontWeight: breadcrumbs.length === 0 ? 600 : 400,
                color:
                  breadcrumbs.length === 0 ? token.colorText : token.colorTextSecondary,
              }}
            >
              {t.composeSaveLocation.rootDirName}
            </Button>

            {breadcrumbs.map((seg, idx) => {
              const target = '/' + breadcrumbs.slice(0, idx + 1).join('/')
              const isLast = idx === breadcrumbs.length - 1
              return (
                <Flex key={target} align="center" gap={4}>
                  <RightOutlined
                    style={{ fontSize: 10, color: token.colorTextQuaternary }}
                  />
                  <Button
                    type="text"
                    size="small"
                    onClick={() => navigateTo(target)}
                    style={{
                      paddingInline: 6,
                      fontFamily: token.fontFamilyCode,
                      fontWeight: isLast ? 600 : 400,
                      color: isLast ? token.colorText : token.colorTextSecondary,
                    }}
                  >
                    {seg}
                  </Button>
                </Flex>
              )
            })}
          </Flex>

          <Button
            size="small"
            icon={<PlusOutlined style={{ color: token.colorPrimary }} />}
            onClick={() => {
              setErrorNotice('')
              setOverwritePrompt(null)
              setMkdirName('')
              setMkdirOpen(true)
            }}
            style={{ flexShrink: 0 }}
          >
            {t.composeSaveLocation.newFolderBtn}
          </Button>
        </Flex>

        {/* 行内新建文件夹输入栏 */}
        {mkdirOpen && (
          <Flex
            align="center"
            gap={8}
            style={{
              padding: '6px 10px',
              borderRadius: token.borderRadius,
              border: `1px solid ${token.colorPrimary}`,
              background: token.colorFillAlter,
            }}
          >
            <FolderOutlined style={{ color: token.colorPrimary, flexShrink: 0 }} />
            <Input
              size="small"
              autoFocus
              value={mkdirName}
              placeholder={t.composeSaveLocation.newFolderPlaceholder}
              style={{ flex: 1, fontFamily: token.fontFamilyCode }}
              onChange={(e) => {
                setMkdirName(e.target.value)
                if (errorNotice) setErrorNotice('')
              }}
              onPressEnter={() => {
                void handleCreateFolder()
              }}
            />
            <Button
              size="small"
              type="primary"
              loading={mkdirSubmitting}
              onClick={() => {
                void handleCreateFolder()
              }}
            >
              {t.composeSaveLocation.createBtn}
            </Button>
            <Button
              size="small"
              onClick={() => {
                setMkdirOpen(false)
                setErrorNotice('')
              }}
            >
              {t.composeSaveLocation.cancelBtn}
            </Button>
          </Flex>
        )}

        {/* 纯净文件夹列表（零干扰标签） */}
        <div
          style={{
            height: 200,
            overflowY: 'auto',
            borderRadius: token.borderRadius,
            border: `1px solid ${token.colorBorderSecondary}`,
            background: token.colorBgContainer,
          }}
        >
          {loading ? (
            <Flex
              align="center"
              justify="center"
              gap={8}
              style={{ height: '100%' }}
            >
              <Spin size="small" />
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t.composeSaveLocation.loadingDirs}
              </Typography.Text>
            </Flex>
          ) : dirs.length === 0 ? (
            <Flex align="center" justify="center" style={{ height: '100%' }}>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t.composeSaveLocation.emptyFolder}
              </Typography.Text>
            </Flex>
          ) : (
            dirs.map((name, idx) => {
              const full = `${curDir === '/' ? '' : curDir}/${name}`
              return (
                <Flex
                  key={full}
                  align="center"
                  justify="space-between"
                  onClick={() => navigateTo(full)}
                  style={{
                    padding: '8px 12px',
                    cursor: 'pointer',
                    borderBottom:
                      idx < dirs.length - 1
                        ? `1px solid ${token.colorSplit}`
                        : 'none',
                    transition: 'background-color 0.12s ease',
                  }}
                  onMouseEnter={(e) => {
                    e.currentTarget.style.backgroundColor = token.colorFillAlter
                  }}
                  onMouseLeave={(e) => {
                    e.currentTarget.style.backgroundColor = 'transparent'
                  }}
                >
                  <Flex align="center" gap={8} style={{ minWidth: 0 }}>
                    <FolderOutlined
                      style={{ color: token.colorPrimary, flexShrink: 0 }}
                    />
                    <Typography.Text
                      ellipsis
                      style={{ fontFamily: token.fontFamilyCode, fontSize: 12.5 }}
                    >
                      {name}
                    </Typography.Text>
                  </Flex>
                  <RightOutlined
                    style={{ fontSize: 10, color: token.colorTextQuaternary }}
                  />
                </Flex>
              )
            })
          )}
        </div>
      </Flex>

      {/* 底部栏 */}
      <div
        style={{
          padding: '10px 16px',
          borderTop: `1px solid ${token.colorSplit}`,
          background: token.colorFillAlter,
        }}
      >
        <Flex vertical gap={8}>
          {showProjSubCheckbox && (
            <Checkbox
              checked={useProjSubfolder}
              onChange={(e) => {
                setUseProjSubfolder(e.target.checked)
                setErrorNotice('')
                setOverwritePrompt(null)
              }}
              style={{ fontSize: 12 }}
            >
              <span>{t.composeSaveLocation.autoSubCheckboxBefore}</span>
              <Typography.Text code style={{ fontSize: 12 }}>
                {safeProjFolder}
              </Typography.Text>
            </Checkbox>
          )}

          {overwritePrompt ? (
            <Flex
              wrap="wrap"
              align="center"
              justify="space-between"
              gap={8}
              style={{
                padding: '6px 10px',
                borderRadius: token.borderRadius,
                border: `1px solid ${token.colorWarning}`,
                background: token.colorBgContainer,
              }}
            >
              <Typography.Text style={{ fontSize: 12 }}>
                {overwritePrompt.kind === 'other_stopped' ? (
                  <>
                    {t.composeSaveLocation.promptOverwriteStoppedBefore}
                    <Typography.Text code style={{ fontSize: 12 }}>
                      {overwritePrompt.owner}
                    </Typography.Text>
                    {t.composeSaveLocation.promptOverwriteStoppedAfter}
                  </>
                ) : overwritePrompt.kind === 'same' ? (
                  t.composeSaveLocation.promptOverwriteSame
                ) : (
                  t.composeSaveLocation.promptOverwriteOther
                )}
              </Typography.Text>
              <Flex align="center" gap={8}>
                <Button
                  size="small"
                  onClick={() => setOverwritePrompt(null)}
                >
                  {t.composeSaveLocation.cancelBtn}
                </Button>
                <Button
                  size="small"
                  type="primary"
                  style={{ background: token.colorWarning, borderColor: token.colorWarning }}
                  onClick={() => onConfirmFile(overwritePrompt.targetFile)}
                >
                  {t.composeSaveLocation.confirmOverwriteBtn}
                </Button>
              </Flex>
            </Flex>
          ) : (
            <Flex align="center" justify="space-between" gap={12}>
              <div style={{ minWidth: 0, flex: 1 }}>
                {errorNotice ? (
                  <Typography.Text type="danger" style={{ fontSize: 12 }}>
                    {errorNotice}
                  </Typography.Text>
                ) : (
                  <Typography.Text
                    type="secondary"
                    ellipsis={{ tooltip: previewTargetFile }}
                    style={{ display: 'block', fontSize: 12 }}
                  >
                    {t.composeSaveLocation.savePreviewPrefix}
                    <Typography.Text
                      style={{ fontFamily: token.fontFamilyCode, fontSize: 12 }}
                    >
                      {previewTargetFile}
                    </Typography.Text>
                  </Typography.Text>
                )}
              </div>

              <Flex align="center" gap={8} style={{ flexShrink: 0 }}>
                <Button size="small" onClick={onClose}>
                  {t.composeSaveLocation.cancelBtn}
                </Button>
                <Button
                  size="small"
                  type="primary"
                  loading={checking}
                  disabled={loading}
                  onClick={() => {
                    void handleConfirm()
                  }}
                >
                  {t.composeSaveLocation.confirmPickBtn}
                </Button>
              </Flex>
            </Flex>
          )}
        </Flex>
      </div>
    </div>
  )
}
