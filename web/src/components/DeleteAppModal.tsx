import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import {
  Alert,
  Button,
  Checkbox,
  Flex,
  Modal,
  Space,
  Spin,
  Tag,
  Typography,
  message,
  theme,
} from 'antd'
import { ExclamationCircleFilled } from '@ant-design/icons'
import {
  api,
  type DeleteAppPreviewResponse,
  type DeleteFileItem,
} from '../api/client'
import { useI18n } from '../i18n'

export interface DeleteAppModalProps {
  open: boolean
  appKey: string
  appName: string
  hostName: string
  source: 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
  isProjectHint?: boolean
  onClose: () => void
  onDeleted: () => void
}

export default function DeleteAppModal(props: DeleteAppModalProps) {
  return (
    <Modal
      open={props.open}
      onCancel={props.onClose}
      footer={null}
      width={520}
      destroyOnHidden
      closable={false}
      title={null}
      styles={{ body: { paddingTop: 4 } }}
    >
      {props.open && <DeleteAppModalBody key={props.appKey} {...props} />}
    </Modal>
  )
}

function DeleteAppModalBody({
  appKey,
  appName,
  hostName,
  source,
  isProjectHint,
  onClose,
  onDeleted,
}: DeleteAppModalProps) {
  const { token } = theme.useToken()
  const { t } = useI18n()
  const queryClient = useQueryClient()

  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')
  const [preview, setPreview] = useState<DeleteAppPreviewResponse | null>(null)

  // 默认勾选同时删除本地镜像，默认不勾选删除关联文件与数据
  const [deleteImages, setDeleteImages] = useState(true)
  const [deleteFiles, setDeleteFiles] = useState(false)
  const [selectedFileIds, setSelectedFileIds] = useState<string[]>([])
  const [secondConfirmOpen, setSecondConfirmOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    let cancelled = false
    api
      .previewDeleteApp(appKey)
      .then((res) => {
        if (cancelled) return
        setPreview(res)
        const defaults = res.files.filter((f) => !f.protected && f.defaultSelect).map((f) => f.id)
        setSelectedFileIds(defaults)
      })
      .catch((e) => {
        if (cancelled) return
        setLoadErr((e as Error).message)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [appKey])

  const isProject =
    preview?.isProject ??
    Boolean(isProjectHint || source === 'compose' || source === 'dsm_project')
  const targetName = preview?.appName || appName

  const files = useMemo(() => preview?.files ?? [], [preview?.files])
  const cleanableFiles = useMemo(() => files.filter((f) => !f.protected), [files])
  const images = useMemo(() => preview?.images ?? [], [preview?.images])
  const containers = useMemo(() => preview?.containers ?? [], [preview?.containers])

  const effectiveFileItems = useMemo(() => {
    if (!deleteFiles) return []
    const idSet = new Set(selectedFileIds)
    return cleanableFiles.filter((f) => idSet.has(f.id))
  }, [cleanableFiles, deleteFiles, selectedFileIds])

  const handleToggleFilesMaster = (checked: boolean) => {
    setDeleteFiles(checked)
    if (checked && selectedFileIds.length === 0) {
      const defaults = cleanableFiles.filter((f) => f.defaultSelect).map((f) => f.id)
      if (defaults.length > 0) {
        setSelectedFileIds(defaults)
      } else if (cleanableFiles.length === 1) {
        setSelectedFileIds([cleanableFiles[0].id])
      }
    }
  }

  const handleToggleSubFile = (fileId: string, checked: boolean) => {
    setSelectedFileIds((prev) => {
      const next = checked
        ? Array.from(new Set([...prev, fileId]))
        : prev.filter((id) => id !== fileId)
      if (next.length === 0) {
        setDeleteFiles(false)
      } else {
        setDeleteFiles(true)
      }
      return next
    })
  }

  const executeDelete = async () => {
    setSubmitting(true)
    try {
      await api.deleteApp({
        appKey,
        deleteImages,
        fileIds: effectiveFileItems.map((f) => f.id),
      })
      setSecondConfirmOpen(false)
      message.success(
        (isProject ? t.deleteModal.deletedProjectMsg : t.deleteModal.deletedContainerMsg).replace(
          '{name}',
          targetName,
        ),
      )
      void queryClient.invalidateQueries({ queryKey: ['overview'] })
      void queryClient.invalidateQueries({ queryKey: ['containers'] })
      void queryClient.invalidateQueries({ queryKey: ['containers-fast'] })
      void queryClient.invalidateQueries({ queryKey: ['containers-cached'] })
      onDeleted()
      onClose()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const handlePrimaryClick = () => {
    if (effectiveFileItems.length > 0) {
      setSecondConfirmOpen(true)
      return
    }
    void executeDelete()
  }

  const renderFileTag = (item: DeleteFileItem) => {
    if (item.protected) {
      return (
        <Tag variant="filled" style={{ margin: 0, flexShrink: 0 }}>
          {t.deleteModal.kindProtected}
        </Tag>
      )
    }
    if (item.kind === 'project_dir') {
      return (
        <Tag variant="filled" color="error" style={{ margin: 0, flexShrink: 0 }}>
          {t.deleteModal.kindProjectDir}
        </Tag>
      )
    }
    if (item.kind === 'volume') {
      return (
        <Tag variant="filled" color="orange" style={{ margin: 0, flexShrink: 0 }}>
          {t.deleteModal.kindVolume}
        </Tag>
      )
    }
    if (item.reason === 'external_bind') {
      return (
        <Tag variant="filled" color="warning" style={{ margin: 0, flexShrink: 0 }}>
          {t.deleteModal.kindExternalBind}
        </Tag>
      )
    }
    return (
      <Tag variant="filled" color="error" style={{ margin: 0, flexShrink: 0 }}>
        {t.deleteModal.kindBindMount}
      </Tag>
    )
  }

  const renderFileReason = (item: DeleteFileItem): string => {
    switch (item.reason) {
      case 'system_protected':
        return t.deleteModal.reasonProtected
      case 'external_bind':
        return t.deleteModal.reasonExternal
      case 'readonly_bind':
        return t.deleteModal.reasonReadonly
      case 'dsm_api_host_path_unsupported':
        return t.deleteModal.reasonDsmUnsupported
      default:
        return item.target ? `→ ${item.target}` : ''
    }
  }

  const summaryText = useMemo(() => {
    const baseNoun = isProject ? t.deleteModal.nounProject : t.deleteModal.nounContainer
    const hasImg = deleteImages && images.length > 0
    const fileCnt = effectiveFileItems.length
    if (!hasImg && fileCnt === 0) {
      return isProject ? t.deleteModal.summaryOnlyProject : t.deleteModal.summaryOnlyContainer
    }
    const parts = [baseNoun]
    if (hasImg) parts.push(t.deleteModal.nounImages)
    if (fileCnt > 0) {
      parts.push(t.deleteModal.nounFilesCount.replace('{count}', String(fileCnt)))
    }
    return `${t.deleteModal.summaryPrefix}${parts.join(' + ')}`
  }, [deleteImages, effectiveFileItems.length, images.length, isProject, t.deleteModal])

  const subtitleText = isProject
    ? containers.length > 0
      ? t.deleteModal.subProject
          .replace('{host}', hostName)
          .replace('{count}', String(containers.length))
      : t.deleteModal.subProjectGeneric.replace('{host}', hostName)
    : t.deleteModal.subContainer.replace('{host}', hostName)

  return (
    <>
      <Flex vertical gap={14}>
        {/* 头部标题区 */}
        <Flex
          align="flex-start"
          justify="space-between"
          gap={12}
          style={{
            paddingBottom: 12,
            borderBottom: `1px solid ${token.colorSplit}`,
          }}
        >
          <div style={{ minWidth: 0, flex: 1 }}>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {(isProject ? t.deleteModal.titleProject : t.deleteModal.titleContainer).replace(
                '{name}',
                targetName,
              )}
            </Typography.Title>
            <Typography.Text
              type="secondary"
              style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 4 }}
            >
              {subtitleText}
            </Typography.Text>
          </div>
          {isProject && (
            <Tag variant="filled" color="warning" style={{ margin: 0, flexShrink: 0 }}>
              {t.deleteModal.projectChip}
            </Tag>
          )}
        </Flex>

        {loading ? (
          <Flex align="center" justify="center" gap={10} style={{ padding: '28px 0' }}>
            <Spin size="small" />
            <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
              {t.deleteModal.loadingPreview}
            </Typography.Text>
          </Flex>
        ) : loadErr ? (
          <Alert type="error" showIcon title={loadErr} />
        ) : (
          <Flex vertical gap={10}>
            {/* 整项目包含的容器胶囊清单 */}
            {isProject && containers.length > 0 && (
              <Space size={[6, 6]} wrap>
                {containers.map((c) => (
                  <Tag
                    key={c}
                    style={{
                      margin: 0,
                      fontFamily: token.fontFamilyCode,
                      fontSize: token.fontSizeSM,
                    }}
                  >
                    {c}
                  </Tag>
                ))}
              </Space>
            )}

            {/* 勾选项 1：同时删除本地镜像（默认勾选） */}
            <div
              style={{
                padding: '12px 14px',
                borderRadius: token.borderRadiusLG,
                border: `1px solid ${deleteImages && images.length > 0 ? token.colorPrimaryBorder : token.colorBorderSecondary}`,
                background: token.colorBgContainer,
              }}
            >
              <Flex align="flex-start" gap={10}>
                <Checkbox
                  checked={deleteImages && images.length > 0}
                  disabled={images.length === 0}
                  onChange={(e) => setDeleteImages(e.target.checked)}
                  style={{ marginTop: 2 }}
                />
                <div
                  style={{ flex: 1, minWidth: 0, cursor: images.length > 0 ? 'pointer' : 'default' }}
                  onClick={() => {
                    if (images.length > 0) setDeleteImages((v) => !v)
                  }}
                >
                  <Flex align="center" justify="space-between" gap={8}>
                    <Typography.Text strong>{t.deleteModal.optDeleteImages}</Typography.Text>
                    {images.length > 0 && (
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        {t.deleteModal.imagesCount.replace('{count}', String(images.length))}
                      </Typography.Text>
                    )}
                  </Flex>
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
                  >
                    {t.deleteModal.optDeleteImagesDesc}
                  </Typography.Text>
                  {deleteImages && images.length > 0 && (
                    <div
                      onClick={(e) => e.stopPropagation()}
                      style={{
                        marginTop: 8,
                        padding: '6px 10px',
                        borderRadius: token.borderRadiusSM,
                        background: token.colorFillAlter,
                        maxHeight: 96,
                        overflowY: 'auto',
                      }}
                    >
                      {images.map((img) => (
                        <Typography.Text
                          key={img}
                          ellipsis={{ tooltip: img }}
                          style={{
                            display: 'block',
                            fontFamily: token.fontFamilyCode,
                            fontSize: token.fontSizeSM,
                            color: token.colorTextSecondary,
                          }}
                        >
                          {img}
                        </Typography.Text>
                      ))}
                    </div>
                  )}
                </div>
              </Flex>
            </div>

            {/* 勾选项 2：同时删除关联文件与数据（默认不勾选） */}
            <div
              style={{
                padding: '12px 14px',
                borderRadius: token.borderRadiusLG,
                border: `1px solid ${effectiveFileItems.length > 0 ? token.colorErrorBorder : token.colorBorderSecondary}`,
                background: token.colorBgContainer,
              }}
            >
              <Flex align="flex-start" gap={10}>
                <Checkbox
                  checked={deleteFiles && cleanableFiles.length > 0}
                  disabled={cleanableFiles.length === 0}
                  onChange={(e) => handleToggleFilesMaster(e.target.checked)}
                  style={{ marginTop: 2 }}
                />
                <div
                  style={{
                    flex: 1,
                    minWidth: 0,
                    cursor: cleanableFiles.length > 0 ? 'pointer' : 'default',
                  }}
                  onClick={() => {
                    if (cleanableFiles.length > 0) handleToggleFilesMaster(!deleteFiles)
                  }}
                >
                  <Flex align="center" justify="space-between" gap={8}>
                    <Typography.Text
                      strong
                      style={{
                        color:
                          cleanableFiles.length === 0 ? token.colorTextDisabled : token.colorText,
                      }}
                    >
                      {t.deleteModal.optDeleteFiles}
                    </Typography.Text>
                    {cleanableFiles.length > 0 && (
                      <Typography.Text type="secondary" style={{ fontSize: token.fontSizeSM }}>
                        {t.deleteModal.filesCount.replace('{count}', String(cleanableFiles.length))}
                      </Typography.Text>
                    )}
                  </Flex>
                  <Typography.Text
                    type="secondary"
                    style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 2 }}
                  >
                    {cleanableFiles.length > 0
                      ? t.deleteModal.optDeleteFilesDesc
                      : t.deleteModal.optDeleteFilesEmpty}
                  </Typography.Text>
                </div>
              </Flex>

              {/* 勾选后展开路径与数据卷子项（或仅含受保护路径时展示说明） */}
              {(deleteFiles || (cleanableFiles.length === 0 && files.length > 0)) &&
                files.length > 0 && (
                  <Flex
                    vertical
                    gap={6}
                    style={{
                      marginTop: 10,
                      paddingTop: 10,
                      paddingLeft: 26,
                      borderTop: `1px solid ${token.colorSplit}`,
                      maxHeight: 190,
                      overflowY: 'auto',
                    }}
                  >
                    {files.map((item) => {
                      const subChecked = !item.protected && selectedFileIds.includes(item.id)
                      const note = renderFileReason(item)
                      return (
                        <Flex
                          key={item.id}
                          align="flex-start"
                          gap={8}
                          onClick={() => {
                            if (!item.protected) handleToggleSubFile(item.id, !subChecked)
                          }}
                          style={{
                            padding: '6px 8px',
                            borderRadius: token.borderRadiusSM,
                            background: token.colorFillAlter,
                            cursor: item.protected ? 'not-allowed' : 'pointer',
                            opacity: item.protected ? 0.65 : 1,
                          }}
                        >
                          <Checkbox
                            checked={subChecked}
                            disabled={item.protected}
                            onClick={(e) => e.stopPropagation()}
                            onChange={(e) => handleToggleSubFile(item.id, e.target.checked)}
                            style={{ marginTop: 2 }}
                          />
                          <div style={{ flex: 1, minWidth: 0 }}>
                            <Flex align="center" gap={6} wrap="wrap">
                              {renderFileTag(item)}
                              <Typography.Text
                                style={{
                                  fontFamily: token.fontFamilyCode,
                                  fontSize: token.fontSizeSM,
                                  wordBreak: 'break-all',
                                }}
                              >
                                {item.path}
                              </Typography.Text>
                            </Flex>
                            {note && (
                              <Typography.Text
                                type="secondary"
                                style={{
                                  fontSize: token.fontSizeSM,
                                  display: 'block',
                                  marginTop: 2,
                                }}
                              >
                                {note}
                              </Typography.Text>
                            )}
                          </div>
                        </Flex>
                      )
                    })}
                  </Flex>
                )}
            </div>
          </Flex>
        )}

        {/* 底部操作栏 */}
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
            {summaryText}
          </Typography.Text>
          <Space size={8}>
            <Button onClick={onClose} disabled={submitting}>
              {t.deleteModal.cancelBtn}
            </Button>
            <Button
              type="primary"
              danger
              loading={submitting && !secondConfirmOpen}
              disabled={loading || Boolean(loadErr)}
              onClick={handlePrimaryClick}
            >
              {isProject ? t.deleteModal.confirmProjectBtn : t.deleteModal.confirmContainerBtn}
            </Button>
          </Space>
        </Flex>
      </Flex>

      {/* 勾选删除关联文件与数据后，点击确认时触发二次确认弹窗 */}
      <Modal
        open={secondConfirmOpen}
        onCancel={() => !submitting && setSecondConfirmOpen(false)}
        footer={null}
        width={460}
        closable={false}
        title={null}
        styles={{ body: { paddingTop: 4 } }}
      >
        <Flex vertical gap={14}>
          <Flex align="flex-start" gap={10}>
            <ExclamationCircleFilled
              style={{ color: token.colorError, fontSize: 20, marginTop: 2, flexShrink: 0 }}
            />
            <div style={{ flex: 1, minWidth: 0 }}>
              <Typography.Title level={5} style={{ margin: 0, color: token.colorError }}>
                {t.deleteModal.secondConfirmTitle}
              </Typography.Title>
              <Typography.Text
                type="secondary"
                style={{ fontSize: token.fontSizeSM, display: 'block', marginTop: 6 }}
              >
                {t.deleteModal.secondConfirmDesc}
              </Typography.Text>
            </div>
          </Flex>

          <Flex
            vertical
            gap={6}
            style={{
              padding: '10px 12px',
              borderRadius: token.borderRadius,
              background: token.colorFillAlter,
              border: `1px solid ${token.colorErrorBorder}`,
              maxHeight: 180,
              overflowY: 'auto',
            }}
          >
            {effectiveFileItems.map((item) => (
              <Flex key={item.id} align="center" gap={8}>
                {renderFileTag(item)}
                <Typography.Text
                  style={{
                    fontFamily: token.fontFamilyCode,
                    fontSize: token.fontSizeSM,
                    wordBreak: 'break-all',
                  }}
                >
                  {item.path}
                </Typography.Text>
              </Flex>
            ))}
          </Flex>

          <Flex justify="flex-end" gap={8} style={{ paddingTop: 4 }}>
            <Button onClick={() => setSecondConfirmOpen(false)} disabled={submitting}>
              {t.deleteModal.secondConfirmBack}
            </Button>
            <Button type="primary" danger loading={submitting} onClick={() => void executeDelete()}>
              {t.deleteModal.secondConfirmOk}
            </Button>
          </Flex>
        </Flex>
      </Modal>
    </>
  )
}
