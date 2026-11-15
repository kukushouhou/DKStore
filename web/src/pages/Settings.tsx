import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Card,
  Col,
  Divider,
  Flex,
  Form,
  Input,
  List,
  Progress,
  Row,
  Segmented,
  Select,
  Space,
  Tag,
  Tooltip,
  Typography,
  Upload,
  message,
  theme,
} from 'antd'
import {
  DeleteOutlined,
  GithubOutlined,
  PlusOutlined,
  ReloadOutlined,
  SendOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  api,
  showApiError,
  type AppSettings,
  type RegistryCred,
  type SelfUpdateConfig,
  type SelfUpdateJob,
} from '../api/client'
import SelfUpdateSourceFields from '../components/SelfUpdateSourceFields'
import { useI18n } from '../i18n'
import { useUIStore, type Locale, type ThemeMode } from '../store/ui'

export default function SettingsPage() {
  const { locale, setLocale, t } = useI18n()
  const { token } = theme.useToken()
  const { themeMode, setThemeMode } = useUIStore()
  const qc = useQueryClient()

  const settingsQuery = useQuery({ queryKey: ['settings'], queryFn: api.getSettings })
  const credsQuery = useQuery({ queryKey: ['regCreds'], queryFn: api.listRegCreds })
  const healthQuery = useQuery({
    queryKey: ['health'],
    queryFn: api.health,
    staleTime: 5 * 60 * 1000,
  })
  const selfUpdateQuery = useQuery({
    queryKey: ['selfUpdate', locale],
    queryFn: () => api.selfUpdateStatus(false),
  })

  const [suCfgDraft, setSuCfg] = useState<SelfUpdateConfig | null>(null)
  const suCfg: SelfUpdateConfig = suCfgDraft ??
    selfUpdateQuery.data?.config ?? {
      policy: 'auto',
      repo: 'kukushouhou/DKStore',
      channel: 'github',
      customHost: '',
      customMode: 'prefix',
    }
  const [savingSuCfg, setSavingSuCfg] = useState(false)
  const [recheckingSu, setRecheckingSu] = useState(false)
  const [uploadingPkg, setUploadingPkg] = useState(false)
  const [showNotes, setShowNotes] = useState(false)
  const [safetyModalOpen, setSafetyModalOpen] = useState(false)
  const [localJob, setLocalJob] = useState<SelfUpdateJob | null>(null)
  const activeJob = localJob ?? selfUpdateQuery.data?.job ?? null

  useEffect(() => {
    if (!activeJob || (activeJob.state !== 'running' && activeJob.state !== 'restarting')) {
      return
    }
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined

    const pollRunning = async () => {
      try {
        const st = await api.selfUpdateStatus(false)
        if (cancelled) return
        if (st.job) {
          setLocalJob(st.job)
          if (st.job.state === 'running') {
            timer = setTimeout(pollRunning, 550)
          }
        }
      } catch {
        if (!cancelled) {
          setLocalJob((prev) => {
            const base = prev ?? activeJob
            return base ? { ...base, state: 'restarting', phase: 'restarting', percent: 100 } : null
          })
        }
      }
    }

    const pollRestarting = async (attempt: number) => {
      try {
        const h = await api.health()
        if (cancelled) return
        if (h.status === 'ok') {
          message.success(t.selfUpdate.upgradedReloading)
          setTimeout(() => window.location.reload(), 300)
          return
        }
      } catch {
        // 切换瞬间连接重置属正常现象，继续轮询
      }
      if (!cancelled && attempt < 40) {
        timer = setTimeout(() => void pollRestarting(attempt + 1), 650)
      }
    }

    if (activeJob.state === 'running') {
      timer = setTimeout(pollRunning, 550)
    } else if (activeJob.state === 'restarting') {
      timer = setTimeout(() => void pollRestarting(0), 950)
    }

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [activeJob, t.selfUpdate.upgradedReloading])

  const [form] = Form.useForm<AppSettings>()
  const [credForm] = Form.useForm<RegistryCred>()
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [saving, setSaving] = useState(false)
  const [testingNotify, setTestingNotify] = useState(false)

  useEffect(() => {
    if (settingsQuery.data) {
      form.setFieldsValue(settingsQuery.data)
    }
  }, [settingsQuery.data, form])

  const channelVal = Form.useWatch('channel', form)

  const onSaveSettings = async () => {
    const vals = form.getFieldsValue(true) as AppSettings
    vals.locale = locale
    vals.progressStyle = useUIStore.getState().progressStyle
    setSaving(true)
    try {
      await api.saveSettings(vals)
      message.success(t.settings.settingsSaved)
      qc.invalidateQueries({ queryKey: ['settings'] })
    } catch (e) {
      showApiError(e)
    } finally {
      setSaving(false)
    }
  }

  const onTestNotify = async () => {
    const vals = form.getFieldsValue(true) as AppSettings
    vals.locale = locale
    setTestingNotify(true)
    try {
      await api.testNotify(vals)
      message.success(t.settings.testSent)
    } catch (e) {
      showApiError(e)
    } finally {
      setTestingNotify(false)
    }
  }

  const onAddCred = async () => {
    const vals = await credForm.validateFields()
    try {
      await api.saveRegCred(vals)
      credForm.resetFields()
      qc.invalidateQueries({ queryKey: ['regCreds'] })
      message.success(t.settings.settingsSaved)
    } catch (e) {
      showApiError(e)
    }
  }

  const onUpdatePw = async () => {
    if (!oldPw || !newPw) {
      message.error(t.settings.pwRequired)
      return
    }
    if ([...newPw].length < 6) {
      message.error(t.settings.pwTooShort)
      return
    }
    if (newPw !== confirmPw) {
      message.error(t.settings.pwMismatch)
      return
    }
    try {
      await api.setPassword(oldPw, newPw)
      setOldPw('')
      setNewPw('')
      setConfirmPw('')
      qc.invalidateQueries({ queryKey: ['authStatus'] })
      message.success(t.settings.pwUpdated)
    } catch (e) {
      showApiError(e)
    }
  }

  const switchLang = async (next: Locale) => {
    setLocale(next)
    if (settingsQuery.data) {
      await api.saveSettings({ ...settingsQuery.data, locale: next })
    }
  }

  const onRefreshSelfUpdate = async () => {
    setRecheckingSu(true)
    try {
      const fresh = await api.selfUpdateStatus(true)
      qc.setQueryData(['selfUpdate', locale], fresh)
    } catch (e) {
      showApiError(e)
    } finally {
      setRecheckingSu(false)
    }
  }

  const onSaveSelfUpdateCfg = async () => {
    setSavingSuCfg(true)
    try {
      const res = await api.saveSelfUpdateConfig(suCfg)
      setSuCfg(res.config)
      message.success(t.selfUpdate.savedMsg)
      const fresh = await api.selfUpdateStatus(true)
      qc.setQueryData(['selfUpdate', locale], fresh)
    } catch (e) {
      showApiError(e)
    } finally {
      setSavingSuCfg(false)
    }
  }

  const onApplyOnline = async () => {
    try {
      const res = await api.applySelfUpdate()
      setLocalJob(res.job)
    } catch (e) {
      showApiError(e)
    }
  }

  const onUploadPackage = async (file: File) => {
    setUploadingPkg(true)
    try {
      const res = await api.uploadSelfUpdate(file)
      setLocalJob(res.job)
    } catch (e) {
      showApiError(e)
    } finally {
      setUploadingPkg(false)
    }
  }

  const isUpgrading = activeJob?.state === 'running' || activeJob?.state === 'restarting'
  const phaseLabel =
    activeJob?.phase === 'restarting'
      ? t.selfUpdate.phaseRestarting
      : activeJob?.phase === 'installing'
        ? t.selfUpdate.phaseInstalling
        : activeJob?.phase === 'verifying'
          ? t.selfUpdate.phaseVerifying
          : t.selfUpdate.phaseDownloading
  const pkgSizeMB = selfUpdateQuery.data?.assetSize
    ? ` (${(selfUpdateQuery.data.assetSize / (1024 * 1024)).toFixed(1)} MB)`
    : ''

  return (
    <Space orientation="vertical" size="large" style={{ width: '100%' }}>
      <Typography.Title level={3} style={{ margin: 0 }}>
        {t.settings.title}
      </Typography.Title>

      <Card
        title={t.selfUpdate.cardTitle}
        extra={
          <Space size={8}>
            <Upload
              showUploadList={false}
              beforeUpload={(file) => {
                void onUploadPackage(file)
                return false
              }}
            >
              <Button
                size="small"
                icon={<UploadOutlined />}
                loading={uploadingPkg}
                disabled={isUpgrading}
              >
                {t.selfUpdate.uploadBtn}
              </Button>
            </Upload>
            <Tooltip title={t.selfUpdate.checkBtnTip}>
              <Button
                size="small"
                icon={<ReloadOutlined />}
                loading={recheckingSu || selfUpdateQuery.isFetching}
                disabled={isUpgrading}
                onClick={onRefreshSelfUpdate}
              />
            </Tooltip>
          </Space>
        }
      >
        <Space orientation="vertical" size={14} style={{ width: '100%' }}>
          {selfUpdateQuery.data?.rolledBackError && (
            <Alert
              type="warning"
              showIcon
              title={selfUpdateQuery.data.rolledBackError}
            />
          )}

          <Flex align="center" justify="space-between" wrap gap={8}>
            <Space wrap>
              <Typography.Text type="secondary">{t.settings.curVersionLabel}:</Typography.Text>
              <Tag style={{ fontFamily: token.fontFamilyCode, margin: 0 }}>
                {selfUpdateQuery.data?.currentVersion || healthQuery.data?.version || '-'}
              </Tag>
              <Typography.Text type="secondary">{t.settings.latestVersionLabel}:</Typography.Text>
              {selfUpdateQuery.data ? (
                <>
                  <Tag
                    color={selfUpdateQuery.data.hasUpdate ? 'processing' : 'success'}
                    style={{ fontFamily: token.fontFamilyCode, margin: 0 }}
                  >
                    {selfUpdateQuery.data.latestVersion || selfUpdateQuery.data.currentVersion}
                  </Tag>
                  <Tag
                    color={selfUpdateQuery.data.hasUpdate ? 'processing' : 'success'}
                    style={{ margin: 0 }}
                  >
                    {selfUpdateQuery.data.hasUpdate
                      ? t.settings.newVersionBadge
                      : t.settings.upToDateBadge}
                  </Tag>
                </>
              ) : (
                <Tag style={{ margin: 0 }}>{t.home.checking}</Tag>
              )}
              {selfUpdateQuery.data?.releaseNotes ? (
                <Button
                  size="small"
                  type="link"
                  style={{ padding: 0 }}
                  onClick={() => setShowNotes((v) => !v)}
                >
                  {t.selfUpdate.notesToggle}
                </Button>
              ) : (
                selfUpdateQuery.data?.releaseUrl && (
                  <Button
                    size="small"
                    type="link"
                    icon={<GithubOutlined />}
                    href={selfUpdateQuery.data.releaseUrl}
                    target="_blank"
                    rel="noreferrer"
                  >
                    {t.settings.viewReleaseBtn}
                  </Button>
                )
              )}
            </Space>

            {selfUpdateQuery.data?.hasUpdate && (
              <Button type="primary" loading={isUpgrading} onClick={onApplyOnline}>
                {t.selfUpdate.updateNowBtn}
                {pkgSizeMB}
              </Button>
            )}
          </Flex>

          {showNotes && selfUpdateQuery.data?.releaseNotes && (
            <div
              style={{
                padding: '10px 14px',
                borderRadius: token.borderRadius,
                background: token.colorFillAlter,
                border: `1px solid ${token.colorBorderSecondary}`,
                fontSize: token.fontSizeSM,
                whiteSpace: 'pre-wrap',
                maxHeight: 220,
                overflowY: 'auto',
              }}
            >
              {selfUpdateQuery.data.releaseNotes}
            </div>
          )}

          {activeJob && activeJob.state !== 'idle' && (
            <div style={{ maxWidth: 460 }}>
              <Typography.Text
                type={activeJob.state === 'failed' ? 'danger' : 'secondary'}
                style={{ fontSize: token.fontSizeSM, display: 'block', marginBottom: 4 }}
              >
                {activeJob.state === 'failed' ? activeJob.error : phaseLabel}
              </Typography.Text>
              <Progress
                percent={activeJob.percent}
                size="small"
                status={activeJob.state === 'failed' ? 'exception' : 'active'}
              />
            </div>
          )}

          <Divider style={{ margin: '2px 0' }} />

          <SelfUpdateSourceFields
            layout="horizontal"
            value={suCfg}
            onChange={setSuCfg}
            defaultRepo={selfUpdateQuery.data?.defaultRepo}
            officialMirror={selfUpdateQuery.data?.officialMirror}
            presetMirrors={selfUpdateQuery.data?.presetMirrors}
            onSave={onSaveSelfUpdateCfg}
            saving={savingSuCfg}
            safetyModalOpen={safetyModalOpen}
            onSafetyModalChange={setSafetyModalOpen}
            showInlineSafetyLink
          />
        </Space>
      </Card>

      <Card title={t.settings.appearanceAndLang}>
        <Space direction="vertical" size="middle">
          <Space wrap>
            <Typography.Text style={{ minWidth: 200, display: 'inline-block' }}>
              {t.settings.language}
            </Typography.Text>
            <Segmented
              value={locale}
              onChange={(v) => switchLang(v as Locale)}
              options={[
                { label: '简体中文 (zh-CN)', value: 'zh-CN' },
                { label: 'English (en-US)', value: 'en-US' },
              ]}
            />
          </Space>
          <Space wrap>
            <Typography.Text style={{ minWidth: 200, display: 'inline-block' }}>
              {t.settings.theme}
            </Typography.Text>
            <Segmented
              value={themeMode}
              onChange={(v) => setThemeMode(v as ThemeMode)}
              options={[
                { label: t.settings.themeSystem, value: 'system' },
                { label: t.settings.themeLight, value: 'light' },
                { label: t.settings.themeDark, value: 'dark' },
              ]}
            />
          </Space>
        </Space>
      </Card>

      <Card title={`${t.settings.scheduleTitle} & ${t.settings.notifyTitle}`}>
        <Form form={form} layout="vertical">
          <Form.Item name="checkIntervalHours" label={t.settings.intervalLabel}>
            <Select
              style={{ maxWidth: 280 }}
              options={[
                { label: t.settings.intervalOff, value: 0 },
                ...[1, 3, 6, 12, 24].map((n) => ({
                  label: t.settings.intervalHours.replace('{n}', String(n)),
                  value: n,
                })),
              ]}
            />
          </Form.Item>

          <Form.Item name="channel" label={t.settings.channelLabel}>
            <Select
              style={{ maxWidth: 320 }}
              options={[
                { label: t.settings.channelNone, value: '' },
                { label: t.settings.channelWebhook, value: 'webhook' },
                { label: t.settings.channelWecom, value: 'wecom' },
                { label: t.settings.channelBark, value: 'bark' },
                { label: t.settings.channelTelegram, value: 'telegram' },
                { label: t.settings.channelNtfy, value: 'ntfy' },
              ]}
            />
          </Form.Item>

          {channelVal && channelVal !== 'telegram' && (
            <Form.Item name="webhookUrl" label={t.settings.webhookUrlLabel}>
              <Input placeholder="https://..." />
            </Form.Item>
          )}

          {channelVal === 'telegram' && (
            <Row gutter={12}>
              <Col xs={24} md={12}>
                <Form.Item name="telegramBotToken" label={t.settings.tgTokenLabel}>
                  <Input.Password />
                </Form.Item>
              </Col>
              <Col xs={24} md={12}>
                <Form.Item name="telegramChatId" label={t.settings.tgChatIdLabel}>
                  <Input />
                </Form.Item>
              </Col>
            </Row>
          )}

          <Space>
            <Button type="primary" loading={saving} onClick={onSaveSettings}>
              {t.settings.saveSettings}
            </Button>
            {channelVal && (
              <Button icon={<SendOutlined />} loading={testingNotify} onClick={onTestNotify}>
                {t.settings.testNotify}
              </Button>
            )}
          </Space>
        </Form>
      </Card>

      <Card title={t.settings.regCredsTitle}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t.settings.regCredsDesc}</Typography.Text>
          <Form form={credForm} layout="inline" style={{ rowGap: 8 }}>
            <Form.Item name="registry" rules={[{ required: true }]}>
              <Input placeholder={t.settings.regDomain} />
            </Form.Item>
            <Form.Item name="username" rules={[{ required: true }]}>
              <Input placeholder={t.settings.regUser} />
            </Form.Item>
            <Form.Item name="secret" rules={[{ required: true }]}>
              <Input.Password placeholder={t.settings.regSecret} />
            </Form.Item>
            <Form.Item>
              <Button icon={<PlusOutlined />} onClick={onAddCred}>
                {t.settings.addCred}
              </Button>
            </Form.Item>
          </Form>

          <List
            size="small"
            dataSource={credsQuery.data?.items ?? []}
            renderItem={(item) => (
              <List.Item
                actions={[
                  <Button
                    key="del"
                    type="text"
                    danger
                    size="small"
                    icon={<DeleteOutlined />}
                    onClick={async () => {
                      await api.deleteRegCred(item.registry)
                      qc.invalidateQueries({ queryKey: ['regCreds'] })
                    }}
                  />,
                ]}
              >
                <Space>
                  <Typography.Text strong>{item.registry}</Typography.Text>
                  <Typography.Text type="secondary">({item.username})</Typography.Text>
                </Space>
              </List.Item>
            )}
          />
        </Space>
      </Card>

      <Card title={t.settings.securityTitle}>
        <Row gutter={[12, 12]} style={{ maxWidth: 860 }}>
          <Col xs={24} md={7}>
            <Input.Password
              placeholder={t.settings.oldPw}
              value={oldPw}
              onChange={(e) => setOldPw(e.target.value)}
              autoComplete="current-password"
            />
          </Col>
          <Col xs={24} md={7}>
            <Input.Password
              placeholder={t.settings.newPw}
              value={newPw}
              onChange={(e) => setNewPw(e.target.value)}
              autoComplete="new-password"
            />
          </Col>
          <Col xs={24} md={7}>
            <Input.Password
              placeholder={t.settings.confirmPw}
              value={confirmPw}
              status={confirmPw && confirmPw !== newPw ? 'error' : undefined}
              onChange={(e) => setConfirmPw(e.target.value)}
              onPressEnter={onUpdatePw}
              autoComplete="new-password"
            />
          </Col>
          <Col xs={24} md={3}>
            <Button onClick={onUpdatePw}>
              {t.settings.updatePw}
            </Button>
          </Col>
        </Row>
      </Card>
    </Space>
  )
}
