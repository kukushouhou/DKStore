import { useState } from 'react'
import {
  Button,
  Card,
  Col,
  Divider,
  Flex,
  Input,
  InputNumber,
  Modal,
  Row,
  Segmented,
  Space,
  Tag,
  Typography,
  message,
  theme,
} from 'antd'
import {
  ArrowRightOutlined,
  CopyOutlined,
  DesktopOutlined,
  DisconnectOutlined,
  GlobalOutlined,
  HddOutlined,
  InfoCircleOutlined,
  LockOutlined,
} from '@ant-design/icons'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError, type AuthStatus, type HostInput } from '../api/client'
import { useI18n } from '../i18n'
import type { Locale } from '../store/ui'

const MIN_PW = 6

interface Props {
  authStatus?: AuthStatus
  onStepChange?: (inStep2: boolean) => void
  onDone: () => void
}

const COMPOSE_SNIPPET = `volumes:
  - ./data:/data
  - /var/run/docker.sock:/var/run/docker.sock`

const DOCKER_RUN_SNIPPET = `-v /var/run/docker.sock:/var/run/docker.sock`

export default function SetupPage({ authStatus, onStepChange, onDone }: Props) {
  const { locale, setLocale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const [step, setStep] = useState<1 | 2>(1)
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [saving, setSaving] = useState(false)

  // 步骤 2（套件安装模式：当场绑定本机表单状态）
  const [spkMode, setSpkMode] = useState<'dsm_api' | 'ssh'>('dsm_api')
  const [hostName, setHostName] = useState('')
  const [dsmUrl, setDsmUrl] = useState('http://127.0.0.1:5000')
  const [sshAddress, setSshAddress] = useState('127.0.0.1')
  const [sshPort, setSshPort] = useState<number>(22)
  const [username, setUsername] = useState('')
  const [secret, setSecret] = useState('')
  const [connecting, setConnecting] = useState(false)
  const [whyModalOpen, setWhyModalOpen] = useState(false)

  // 2FA 即时弹窗状态
  const [otpModalOpen, setOtpModalOpen] = useState(false)
  const [otpCode, setOtpCode] = useState('')
  const [deviceTokenNonce, setDeviceTokenNonce] = useState<string | undefined>(undefined)

  // 步骤 2（Docker 安装模式：开启步骤指南弹窗状态）
  const [dockerGuideOpen, setDockerGuideOpen] = useState(false)

  const tooShort = pw.length > 0 && [...pw].length < MIN_PW
  const mismatch = pw2.length > 0 && pw !== pw2

  const onSubmitPw = async () => {
    if ([...pw].length < MIN_PW) {
      message.error(t.settings.pwTooShort)
      return
    }
    if (pw !== pw2) {
      message.error(t.settings.pwMismatch)
      return
    }
    setSaving(true)
    try {
      await api.setupPassword(pw)
      setPw('')
      setPw2('')
      message.success(t.auth.setupDone)
      if (authStatus?.hasLocalHost) {
        onStepChange?.(false)
        await qc.invalidateQueries()
        onDone()
      } else {
        setHostName(t.auth.spkDefaultName)
        onStepChange?.(true)
        setStep(2)
        await qc.invalidateQueries()
      }
    } catch (e) {
      message.error((e as Error).message)
      qc.invalidateQueries({ queryKey: ['authStatus'] })
    } finally {
      setSaving(false)
    }
  }

  const submitSpkHost = async (extraOtp?: string) => {
    const finalName = hostName.trim() || t.auth.spkDefaultName
    const payload: HostInput =
      spkMode === 'dsm_api'
        ? {
            name: finalName,
            kind: 'synology',
            transport: 'dsm_api',
            dsmUrl: dsmUrl.trim() || 'http://127.0.0.1:5000',
            dsmUser: username.trim(),
            dsmSecret: secret,
            dsmOtpCode: extraOtp?.trim() || undefined,
            deviceTokenNonce,
          }
        : {
            name: finalName,
            kind: authStatus?.installMode === 'synology' ? 'synology' : 'docker',
            transport: 'ssh',
            address: sshAddress.trim() || '127.0.0.1',
            port: sshPort || 22,
            sshUser: username.trim(),
            sshAuth: 'password',
            sshSecret: secret,
          }

    setConnecting(true)
    try {
      await api.createHost(payload)
      setOtpModalOpen(false)
      setOtpCode('')
      message.success(t.hosts.saved)
      onStepChange?.(false)
      await qc.invalidateQueries()
      onDone()
    } catch (e) {
      if (e instanceof ApiError && (e.code === 'need_otp' || e.testResult?.needOtp)) {
        if (e.testResult?.deviceTokenNonce) {
          setDeviceTokenNonce(e.testResult.deviceTokenNonce)
        }
        setOtpCode('')
        setOtpModalOpen(true)
        return
      }
      message.error((e as Error).message)
    } finally {
      setConnecting(false)
    }
  }

  const finishAndSkip = () => {
    onStepChange?.(false)
    onDone()
  }

  // 步骤 2
  if (step === 2) {
    const isDockerMode = authStatus?.installMode === 'docker'

    // 分支 B：Docker 安装模式（未挂载本机容器访问权限）
    if (isDockerMode) {
      return (
        <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
          <Card style={{ width: '100%', maxWidth: 580, borderRadius: 16 }}>
            <Space direction="vertical" size={20} style={{ width: '100%' }}>
              {/* 顶部标题栏 */}
              <Flex justify="space-between" align="flex-start" gap={12}>
                <Flex align="center" gap={12}>
                  <div
                    style={{
                      width: 40,
                      height: 40,
                      borderRadius: 12,
                      background: token.colorWarningBg,
                      color: token.colorWarning,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                      fontSize: 20,
                      flexShrink: 0,
                    }}
                  >
                    <DisconnectOutlined />
                  </div>
                  <div>
                    <Space size={8} align="center">
                      <Typography.Title level={5} style={{ margin: 0 }}>
                        {t.auth.dockerTitle}
                      </Typography.Title>
                      <Tag color="processing" style={{ margin: 0, borderRadius: 999 }}>
                        {t.auth.stepBadge}
                      </Tag>
                    </Space>
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {t.auth.dockerSubtitle}
                      </Typography.Text>
                    </div>
                  </div>
                </Flex>
                <Segmented
                  size="small"
                  value={locale}
                  onChange={(v) => setLocale(v as Locale)}
                  options={[
                    { label: '中文', value: 'zh-CN' },
                    { label: 'EN', value: 'en-US' },
                  ]}
                />
              </Flex>

              {/* 对称双卡片 */}
              <Row gutter={[12, 12]} align="stretch">
                <Col xs={24} sm={12} style={{ display: 'flex' }}>
                  <Card
                    size="small"
                    style={{
                      width: '100%',
                      borderRadius: 12,
                      background: token.colorFillQuaternary,
                      display: 'flex',
                      flexDirection: 'column',
                    }}
                    styles={{
                      body: {
                        flex: 1,
                        display: 'flex',
                        flexDirection: 'column',
                        justifyContent: 'space-between',
                        padding: 16,
                      },
                    }}
                  >
                    <div>
                      <div
                        style={{
                          width: 32,
                          height: 32,
                          borderRadius: 8,
                          background: token.colorPrimaryBg,
                          color: token.colorPrimary,
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                          fontSize: 16,
                          marginBottom: 10,
                        }}
                      >
                        <DesktopOutlined />
                      </div>
                      <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
                        {t.auth.dockerCardLocalTitle}
                      </Typography.Text>
                      <Typography.Text type="secondary" style={{ fontSize: 12, lineHeight: 1.6 }}>
                        {t.auth.dockerCardLocalDesc}
                      </Typography.Text>
                    </div>
                    <div
                      style={{
                        marginTop: 12,
                        paddingTop: 10,
                        borderTop: `1px solid ${token.colorBorderSecondary}`,
                      }}
                    >
                      <Button
                        type="link"
                        size="small"
                        style={{ padding: 0, height: 'auto', fontSize: 12 }}
                        onClick={() => setDockerGuideOpen(true)}
                      >
                        {t.auth.dockerCardLocalLink} <ArrowRightOutlined />
                      </Button>
                    </div>
                  </Card>
                </Col>

                <Col xs={24} sm={12} style={{ display: 'flex' }}>
                  <Card
                    size="small"
                    style={{
                      width: '100%',
                      borderRadius: 12,
                      background: token.colorFillQuaternary,
                      display: 'flex',
                      flexDirection: 'column',
                    }}
                    styles={{
                      body: {
                        flex: 1,
                        display: 'flex',
                        flexDirection: 'column',
                        justifyContent: 'space-between',
                        padding: 16,
                      },
                    }}
                  >
                    <div>
                      <div
                        style={{
                          width: 32,
                          height: 32,
                          borderRadius: 8,
                          background: token.colorSuccessBg,
                          color: token.colorSuccess,
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                          fontSize: 16,
                          marginBottom: 10,
                        }}
                      >
                        <GlobalOutlined />
                      </div>
                      <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
                        {t.auth.dockerCardRemoteTitle}
                      </Typography.Text>
                      <Typography.Text type="secondary" style={{ fontSize: 12, lineHeight: 1.6 }}>
                        {t.auth.dockerCardRemoteDesc}
                      </Typography.Text>
                    </div>
                    <div
                      style={{
                        marginTop: 12,
                        paddingTop: 10,
                        borderTop: `1px solid ${token.colorBorderSecondary}`,
                      }}
                    >
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {t.auth.dockerCardRemoteFooter}
                      </Typography.Text>
                    </div>
                  </Card>
                </Col>
              </Row>

              <Divider style={{ margin: 0 }} />

              <Flex justify="flex-end">
                <Button type="primary" onClick={finishAndSkip}>
                  {t.auth.dockerSkipBtn}
                </Button>
              </Flex>
            </Space>
          </Card>

          {/* Docker 开启本机容器访问权限弹窗 */}
          <Modal
            open={dockerGuideOpen}
            title={t.auth.dockerGuideTitle}
            onCancel={() => setDockerGuideOpen(false)}
            footer={
              <Button type="primary" onClick={() => setDockerGuideOpen(false)}>
                {t.auth.dockerGuideOk}
              </Button>
            }
          >
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              <Card size="small" style={{ background: token.colorFillAlter, borderRadius: 10 }}>
                <Flex justify="space-between" align="center" style={{ marginBottom: 4 }}>
                  <Typography.Text strong style={{ fontSize: 13 }}>
                    {t.auth.dockerGuideMethod1Title}
                  </Typography.Text>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard.writeText(COMPOSE_SNIPPET)
                      message.success(t.detail.copied)
                    }}
                  >
                    {t.detail.copyCmd}
                  </Button>
                </Flex>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
                  {t.auth.dockerGuideMethod1Desc}
                </Typography.Paragraph>
                <pre
                  style={{
                    margin: 0,
                    padding: '8px 12px',
                    borderRadius: 8,
                    background: token.colorBgContainer,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    fontFamily: 'monospace',
                    fontSize: 12,
                    overflowX: 'auto',
                  }}
                >
                  {COMPOSE_SNIPPET}
                </pre>
              </Card>

              <Card size="small" style={{ background: token.colorFillAlter, borderRadius: 10 }}>
                <Flex justify="space-between" align="center" style={{ marginBottom: 4 }}>
                  <Typography.Text strong style={{ fontSize: 13 }}>
                    {t.auth.dockerGuideMethod2Title}
                  </Typography.Text>
                  <Button
                    size="small"
                    type="text"
                    icon={<CopyOutlined />}
                    onClick={() => {
                      navigator.clipboard.writeText(DOCKER_RUN_SNIPPET)
                      message.success(t.detail.copied)
                    }}
                  >
                    {t.detail.copyCmd}
                  </Button>
                </Flex>
                <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 8 }}>
                  {t.auth.dockerGuideMethod2Desc}
                </Typography.Paragraph>
                <pre
                  style={{
                    margin: 0,
                    padding: '8px 12px',
                    borderRadius: 8,
                    background: token.colorBgContainer,
                    border: `1px solid ${token.colorBorderSecondary}`,
                    fontFamily: 'monospace',
                    fontSize: 12,
                    overflowX: 'auto',
                  }}
                >
                  {DOCKER_RUN_SNIPPET}
                </pre>
              </Card>
            </Space>
          </Modal>
        </Flex>
      )
    }

    // 分支 A：套件安装模式（当场绑定本机表单）
    return (
      <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
        <Card style={{ width: '100%', maxWidth: 520, borderRadius: 16 }}>
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {/* 顶部标题栏 */}
            <Flex justify="space-between" align="flex-start" gap={12}>
              <Flex align="center" gap={12}>
                <div
                  style={{
                    width: 40,
                    height: 40,
                    borderRadius: 12,
                    background: token.colorPrimaryBg,
                    color: token.colorPrimary,
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    fontSize: 20,
                    flexShrink: 0,
                  }}
                >
                  <HddOutlined />
                </div>
                <div>
                  <Space size={8} align="center">
                    <Typography.Title level={5} style={{ margin: 0 }}>
                      {t.auth.spkTitle}
                    </Typography.Title>
                    <Tag color="processing" style={{ margin: 0, borderRadius: 999 }}>
                      {t.auth.stepBadge}
                    </Tag>
                  </Space>
                  <div>
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {t.auth.spkSubtitle}
                    </Typography.Text>
                  </div>
                </div>
              </Flex>
              <Segmented
                size="small"
                value={locale}
                onChange={(v) => setLocale(v as Locale)}
                options={[
                  { label: '中文', value: 'zh-CN' },
                  { label: 'EN', value: 'en-US' },
                ]}
              />
            </Flex>

            {/* 连接方式切换 */}
            <Segmented
              block
              value={spkMode}
              onChange={(v) => setSpkMode(v as 'dsm_api' | 'ssh')}
              options={[
                { label: t.auth.spkModeWeb, value: 'dsm_api' },
                { label: t.auth.spkModeSsh, value: 'ssh' },
              ]}
            />

            {/* 紧凑表单（不展示任何 2FA 字段） */}
            {spkMode === 'dsm_api' ? (
              <Row gutter={[12, 12]}>
                <Col xs={24} sm={10}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.nameLabel}
                  </Typography.Text>
                  <Input
                    value={hostName}
                    onChange={(e) => setHostName(e.target.value)}
                    placeholder={t.auth.spkDefaultName}
                  />
                </Col>
                <Col xs={24} sm={14}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.auth.spkWebAddrLabel}
                  </Typography.Text>
                  <Input
                    value={dsmUrl}
                    onChange={(e) => setDsmUrl(e.target.value)}
                    style={{ fontFamily: 'monospace' }}
                    placeholder="http://127.0.0.1:5000"
                  />
                </Col>
                <Col xs={24} sm={12}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.dsmUserLabel}
                  </Typography.Text>
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder={t.auth.spkUserPlaceholder}
                  />
                </Col>
                <Col xs={24} sm={12}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.dsmSecretLabel}
                  </Typography.Text>
                  <Input.Password
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    onPressEnter={() => submitSpkHost()}
                  />
                </Col>
              </Row>
            ) : (
              <Row gutter={[12, 12]}>
                <Col xs={24} sm={9}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.nameLabel}
                  </Typography.Text>
                  <Input
                    value={hostName}
                    onChange={(e) => setHostName(e.target.value)}
                    placeholder={t.auth.spkDefaultName}
                  />
                </Col>
                <Col xs={16} sm={10}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.addressLabel}
                  </Typography.Text>
                  <Input
                    value={sshAddress}
                    onChange={(e) => setSshAddress(e.target.value)}
                    style={{ fontFamily: 'monospace' }}
                    placeholder="127.0.0.1"
                  />
                </Col>
                <Col xs={8} sm={5}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.portLabel}
                  </Typography.Text>
                  <InputNumber
                    min={1}
                    max={65535}
                    value={sshPort}
                    onChange={(v) => setSshPort(v || 22)}
                    style={{ width: '100%' }}
                  />
                </Col>
                <Col xs={24} sm={12}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.userLabel}
                  </Typography.Text>
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder="admin / root"
                  />
                </Col>
                <Col xs={24} sm={12}>
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 4 }}>
                    {t.hosts.passwordLabel}
                  </Typography.Text>
                  <Input.Password
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    onPressEnter={() => submitSpkHost()}
                  />
                </Col>
              </Row>
            )}

            <Divider style={{ margin: '4px 0 0' }} />

            {/* 底部操作栏 */}
            <Flex justify="space-between" align="center" wrap gap={8}>
              <Button
                type="link"
                size="small"
                icon={<InfoCircleOutlined />}
                style={{ padding: 0, fontSize: 12 }}
                onClick={() => setWhyModalOpen(true)}
              >
                {t.auth.spkWhyLink}
              </Button>
              <Space size={8}>
                <Button onClick={finishAndSkip}>{t.auth.spkSkipBtn}</Button>
                <Button
                  type="primary"
                  loading={connecting}
                  onClick={() => submitSpkHost()}
                >
                  {t.auth.spkConnectBtn}
                </Button>
              </Space>
            </Flex>
          </Space>
        </Card>

        {/* 为什么需要授权一次本机账号？ */}
        <Modal
          open={whyModalOpen}
          title={t.auth.spkWhyTitle}
          width={420}
          onCancel={() => setWhyModalOpen(false)}
          footer={
            <Button type="primary" onClick={() => setWhyModalOpen(false)}>
              {t.auth.spkWhyOk}
            </Button>
          }
        >
          <Space direction="vertical" size={10}>
            <Typography.Text type="secondary" style={{ fontSize: 13 }}>
              {t.auth.spkWhyP1}
            </Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 13 }}>
              {t.auth.spkWhyP2}
            </Typography.Text>
          </Space>
        </Modal>

        {/* 2FA 即时弹窗 */}
        <Modal
          open={otpModalOpen}
          title={t.hosts.otpModalTitle}
          width={380}
          onCancel={() => setOtpModalOpen(false)}
          footer={
            <Flex justify="flex-end" gap={8}>
              <Button onClick={() => setOtpModalOpen(false)}>{t.ignoreModal.cancel}</Button>
              <Button
                type="primary"
                loading={connecting}
                onClick={() => submitSpkHost(otpCode)}
              >
                {t.hosts.otpVerifySaveBtn}
              </Button>
            </Flex>
          }
        >
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t.hosts.otpModalDesc}
            </Typography.Text>
            <Input
              autoFocus
              maxLength={6}
              placeholder="000000"
              value={otpCode}
              onChange={(e) => setOtpCode(e.target.value)}
              onPressEnter={() => submitSpkHost(otpCode)}
              style={{
                textAlign: 'center',
                letterSpacing: '0.4em',
                fontFamily: 'monospace',
                fontSize: 18,
                padding: '8px 12px',
              }}
            />
          </Space>
        </Modal>
      </Flex>
    )
  }

  // 步骤 1：设置管理员密码
  return (
    <Flex align="center" justify="center" style={{ minHeight: '100vh', padding: 16 }}>
      <Card style={{ width: 420, borderRadius: 16 }}>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Flex justify="space-between" align="center">
            <Typography.Title level={4} style={{ margin: 0 }}>
              <LockOutlined /> {t.auth.setupTitle}
            </Typography.Title>
            <Segmented
              size="small"
              value={locale}
              onChange={(v) => setLocale(v as Locale)}
              options={[
                { label: '中文', value: 'zh-CN' },
                { label: 'EN', value: 'en-US' },
              ]}
            />
          </Flex>
          <Typography.Text type="secondary">{t.auth.setupDesc}</Typography.Text>
          <Input.Password
            placeholder={t.auth.newPw}
            value={pw}
            status={tooShort ? 'error' : undefined}
            onChange={(e) => setPw(e.target.value)}
            autoComplete="new-password"
          />
          <Input.Password
            placeholder={t.auth.confirmPw}
            value={pw2}
            status={mismatch ? 'error' : undefined}
            onChange={(e) => setPw2(e.target.value)}
            onPressEnter={onSubmitPw}
            autoComplete="new-password"
          />
          {(tooShort || mismatch) && (
            <Typography.Text type="danger">
              {tooShort ? t.settings.pwTooShort : t.settings.pwMismatch}
            </Typography.Text>
          )}
          <Button type="primary" block loading={saving} onClick={onSubmitPw}>
            {t.auth.setupBtn}
          </Button>
        </Space>
      </Card>
    </Flex>
  )
}
