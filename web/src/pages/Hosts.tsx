import { useMemo, useState } from 'react'
import {
  Alert,
  Badge,
  Button,
  Card,
  Col,
  Empty,
  Flex,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Radio,
  Row,
  Segmented,
  Space,
  Tag,
  Timeline,
  Tooltip,
  Typography,
  message,
  theme,
} from 'antd'
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudServerOutlined,
  CopyOutlined,
  DeleteOutlined,
  DesktopOutlined,
  EditOutlined,
  HddOutlined,
  KeyOutlined,
  PlusOutlined,
  SafetyCertificateOutlined,
  SyncOutlined,
  WarningOutlined,
} from '@ant-design/icons'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError, type HostInput, type HostItem, type HostTestResult } from '../api/client'
import { useI18n } from '../i18n'

type PlatformId = 'synology' | 'linux' | 'local'

interface PlatformSpec {
  id: PlatformId
  kind: 'synology' | 'docker'
  transports: ('dsm_api' | 'ssh' | 'local')[]
  defaultTransport: 'dsm_api' | 'ssh' | 'local'
}

const PLATFORM_SPECS: PlatformSpec[] = [
  {
    id: 'synology',
    kind: 'synology',
    transports: ['dsm_api', 'ssh'],
    defaultTransport: 'dsm_api',
  },
  {
    id: 'linux',
    kind: 'docker',
    transports: ['ssh'],
    defaultTransport: 'ssh',
  },
  {
    id: 'local',
    kind: 'docker',
    transports: ['local'],
    defaultTransport: 'local',
  },
]

export default function HostsPage() {
  const { locale, t } = useI18n()
  const { token } = theme.useToken()
  const qc = useQueryClient()
  const hostsQuery = useQuery({ queryKey: ['hosts', locale], queryFn: api.listHosts })

  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<HostItem | null>(null)
  const [platformId, setPlatformId] = useState<PlatformId>('synology')
  const [showOtpField, setShowOtpField] = useState(false)
  const [form] = Form.useForm<HostInput>()
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<HostTestResult | null>(null)
  const [generatedPubKey, setGeneratedPubKey] = useState('')
  const [saving, setSaving] = useState(false)

  const canAddLocal = Boolean(hostsQuery.data?.canAddLocal)

  const visiblePlatforms = useMemo(() => {
    return PLATFORM_SPECS.filter((p) => {
      if (p.id === 'local') {
        if (editing) return editing.transport === 'local'
        return canAddLocal
      }
      if (editing && editing.transport === 'local') {
        return false
      }
      return true
    })
  }, [canAddLocal, editing])

  const selectPlatform = (nextId: PlatformId) => {
    const spec = PLATFORM_SPECS.find((p) => p.id === nextId) ?? PLATFORM_SPECS[0]
    setPlatformId(spec.id)
    setTestResult(null)
    form.setFieldsValue({
      kind: spec.kind,
      transport: spec.defaultTransport,
    })
  }

  const openAdd = () => {
    setEditing(null)
    setTestResult(null)
    setGeneratedPubKey('')
    setShowOtpField(false)
    form.resetFields()
    setPlatformId('synology')
    form.setFieldsValue({
      name: '',
      kind: 'synology',
      transport: 'dsm_api',
      port: 22,
      sshAuth: 'password',
    })
    setModalOpen(true)
  }

  const openEdit = (h: HostItem) => {
    setEditing(h)
    setTestResult(null)
    setGeneratedPubKey('')
    setShowOtpField(false)
    form.resetFields()
    const initPlatform: PlatformId =
      h.transport === 'local' ? 'local' : h.kind === 'synology' ? 'synology' : 'linux'
    setPlatformId(initPlatform)
    form.setFieldsValue({
      name: h.name,
      kind: h.kind,
      transport: h.transport,
      address: h.address,
      port: h.port || 22,
      sshUser: h.sshUser,
      sshAuth: h.sshAuth || 'password',
      dsmUrl: h.dsmUrl,
      dsmUser: h.dsmUser,
    })
    setModalOpen(true)
  }

  const runTest = async () => {
    const vals = form.getFieldsValue(true) as HostInput
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api.testHost(editing ? { ...vals, hostId: editing.id } : vals)
      setTestResult(res)
      if (res.needOtp) {
        setShowOtpField(true)
      }
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const handleGenerateKey = async () => {
    try {
      const kp = await api.keygen()
      form.setFieldsValue({ sshAuth: 'key', sshSecret: kp.privateKey })
      setGeneratedPubKey(kp.authorizedKey)
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const handleSave = async () => {
    try {
      await form.validateFields()
    } catch {
      return
    }
    const vals = { ...(form.getFieldsValue(true) as HostInput) }
    if (testResult?.deviceTokenNonce) {
      vals.deviceTokenNonce = testResult.deviceTokenNonce
    }
    delete vals.hostId
    setSaving(true)
    try {
      if (editing) {
        await api.updateHost(editing.id, vals)
      } else {
        await api.createHost(vals)
      }
      message.success(t.hosts.saved)
      setModalOpen(false)
      qc.invalidateQueries({ queryKey: ['hosts'] })
      qc.invalidateQueries({ queryKey: ['overview'] })
      qc.invalidateQueries({ queryKey: ['containers'] })
    } catch (e) {
      if (e instanceof ApiError && e.testResult) {
        setTestResult(e.testResult)
        if (e.testResult.needOtp) {
          setShowOtpField(true)
        }
      }
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const hosts = hostsQuery.data?.items ?? []
  const transportVal = Form.useWatch('transport', form)
  const authVal = Form.useWatch('sshAuth', form)
  const currentSpec = PLATFORM_SPECS.find((p) => p.id === platformId) ?? PLATFORM_SPECS[0]

  const platformMeta = (id: PlatformId) => {
    switch (id) {
      case 'synology':
        return {
          icon: <HddOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformSynology,
          desc: t.hosts.platformSynologyDesc,
        }
      case 'linux':
        return {
          icon: <CloudServerOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformLinux,
          desc: t.hosts.platformLinuxDesc,
        }
      case 'local':
        return {
          icon: <DesktopOutlined style={{ fontSize: 20, color: token.colorPrimary }} />,
          title: t.hosts.platformLocal,
          desc: t.hosts.platformLocalDesc,
        }
    }
  }

  return (
    <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Flex justify="space-between" align="center">
        <Typography.Title level={3} style={{ margin: 0 }}>
          {t.hosts.title}
        </Typography.Title>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
          {t.hosts.addHost}
        </Button>
      </Flex>

      {hosts.length === 0 ? (
        <Empty description={t.hosts.empty} />
      ) : (
        <Row gutter={[16, 16]}>
          {hosts.map((h) => (
            <Col key={h.id} xs={24} md={12}>
              <Card
                title={
                  <Space>
                    <CloudServerOutlined />
                    <span>{h.name}</span>
                  </Space>
                }
                extra={
                  <Space size={8}>
                    {h.sshHostKey && !h.sshHostKeyChangedAt && (
                      <Tooltip title={`${t.hosts.fingerprintTitle}: ${h.sshHostKey}`}>
                        <Tag
                          icon={<SafetyCertificateOutlined />}
                          style={{ cursor: 'pointer', margin: 0 }}
                        >
                          SSH
                        </Tag>
                      </Tooltip>
                    )}
                    <Badge
                      status={
                        h.status === 'online'
                          ? 'success'
                          : h.status === 'offline'
                            ? 'error'
                            : 'default'
                      }
                      text={
                        h.status === 'online'
                          ? t.hosts.online
                          : h.status === 'offline'
                            ? t.hosts.offline
                            : t.hosts.unknown
                      }
                    />
                  </Space>
                }
                actions={[
                  <Button
                    key="scan"
                    type="text"
                    size="small"
                    icon={<SyncOutlined />}
                    onClick={async () => {
                      try {
                        await api.triggerCheck(h.id, true)
                        message.info(t.home.checking)
                      } catch (e) {
                        message.error((e as Error).message)
                      }
                    }}
                  >
                    {t.hosts.scanHost}
                  </Button>,
                  <Button
                    key="edit"
                    type="text"
                    size="small"
                    icon={<EditOutlined />}
                    onClick={() => openEdit(h)}
                  >
                    {t.hosts.editHost}
                  </Button>,
                  <Popconfirm
                    key="del"
                    title={t.hosts.deleteConfirm}
                    onConfirm={async () => {
                      try {
                        await api.deleteHost(h.id)
                        message.success(t.hosts.deleted)
                        qc.invalidateQueries({ queryKey: ['hosts'] })
                        qc.invalidateQueries({ queryKey: ['overview'] })
                        qc.invalidateQueries({ queryKey: ['containers'] })
                      } catch (e) {
                        message.error((e as Error).message)
                      }
                    }}
                  >
                    <Button type="text" danger size="small" icon={<DeleteOutlined />} />
                  </Popconfirm>,
                ]}
              >
                <Space direction="vertical" size={6} style={{ width: '100%' }}>
                  <Space wrap>
                    {h.kind === 'synology' && <Tag color="geekblue">Synology DSM</Tag>}
                    <Tag>
                      {h.transport === 'local'
                        ? t.hosts.localSocket
                        : h.transport === 'dsm_api'
                          ? t.hosts.dsmWebApi
                          : t.hosts.sshRemote}
                    </Tag>
                    {h.transport === 'dsm_api' && h.dsmUrl && (
                      <Typography.Text code>
                        {h.dsmUser}@{h.dsmUrl}
                      </Typography.Text>
                    )}
                    {h.transport === 'ssh' && h.address && (
                      <Typography.Text code>
                        {h.sshUser}@{h.address}:{h.port || 22}
                      </Typography.Text>
                    )}
                  </Space>
                  {h.info && (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      Docker {h.info.dockerVersion} · {h.info.platformVersion || h.info.os} ({h.info.os}/{h.info.arch || t.hosts.archUnknown})
                    </Typography.Text>
                  )}
                  {h.transport === 'ssh' && Boolean(h.sshHostKeyChangedAt) && (
                    <Alert
                      type="warning"
                      showIcon
                      icon={<WarningOutlined />}
                      style={{ padding: '6px 10px', marginTop: 4 }}
                      title={
                        <span style={{ fontSize: 12 }}>
                          {t.hosts.fingerprintChanged}
                          {h.sshHostKeyChangedAt
                            ? ` · ${new Date(h.sshHostKeyChangedAt * 1000).toLocaleString(locale)}`
                            : ''}
                        </span>
                      }
                      description={
                        h.sshPrevHostKey ? (
                          <Typography.Text type="secondary" style={{ fontSize: 11, fontFamily: 'monospace' }}>
                            {t.hosts.fingerprintPrevious}: {h.sshPrevHostKey}
                          </Typography.Text>
                        ) : undefined
                      }
                    />
                  )}
                </Space>
              </Card>
            </Col>
          ))}
        </Row>
      )}

      <Modal
        open={modalOpen}
        title={editing ? t.hosts.editHost : t.hosts.addHost}
        width={620}
        onCancel={() => setModalOpen(false)}
        footer={
          <Flex justify="flex-end" gap={8}>
            <Button onClick={() => setModalOpen(false)}>{t.ignoreModal.cancel}</Button>
            <Button
              icon={<SyncOutlined spin={testing} />}
              loading={testing}
              onClick={runTest}
            >
              {testing ? t.hosts.testing : t.hosts.testBtn}
            </Button>
            <Button type="primary" loading={saving} onClick={handleSave}>
              {t.hosts.saveBtn}
            </Button>
          </Flex>
        }
      >
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {/* 1. 平台选择横向卡片（表驱动） */}
          {visiblePlatforms.length > 1 && (
            <div>
              <Typography.Text strong style={{ display: 'block', marginBottom: 8 }}>
                {t.hosts.platformLabel}
              </Typography.Text>
              <Row gutter={[12, 12]}>
                {visiblePlatforms.map((p) => {
                  const meta = platformMeta(p.id)
                  const active = platformId === p.id
                  return (
                    <Col key={p.id} xs={24} sm={visiblePlatforms.length === 3 ? 8 : 12}>
                      <Card
                        size="small"
                        hoverable
                        onClick={() => selectPlatform(p.id)}
                        style={{
                          height: '100%',
                          borderColor: active ? token.colorPrimary : token.colorBorderSecondary,
                          background: active ? token.colorPrimaryBg : token.colorBgContainer,
                          borderRadius: 10,
                        }}
                      >
                        <Flex align="flex-start" gap={10}>
                          {meta.icon}
                          <div style={{ minWidth: 0 }}>
                            <Typography.Text strong style={{ display: 'block', fontSize: 14 }}>
                              {meta.title}
                            </Typography.Text>
                            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                              {meta.desc}
                            </Typography.Text>
                          </div>
                        </Flex>
                      </Card>
                    </Col>
                  )
                })}
              </Row>
            </div>
          )}

          <Form form={form} layout="vertical" style={{ marginTop: 4 }}>
            <Form.Item name="kind" hidden>
              <Input />
            </Form.Item>
            <Form.Item name="transport" hidden>
              <Input />
            </Form.Item>

            {/* 2. 连接方式切换（当平台支持多种连接方式时显示 Segmented） */}
            {currentSpec.transports.length > 1 && (
              <Form.Item label={t.hosts.connMethodLabel} style={{ marginBottom: 16 }}>
                <Segmented
                  block
                  value={transportVal || currentSpec.defaultTransport}
                  onChange={(v) => {
                    setTestResult(null)
                    form.setFieldsValue({ transport: v as HostInput['transport'] })
                  }}
                  options={currentSpec.transports.map((tr) => ({
                    label: tr === 'dsm_api' ? t.hosts.connDsmApi : t.hosts.connSsh,
                    value: tr,
                  }))}
                />
              </Form.Item>
            )}

            {/* 3. 表单输入区 */}
            <Form.Item name="name" label={t.hosts.nameLabel} rules={[{ required: true }]}>
              <Input placeholder={t.hosts.namePlaceholder} />
            </Form.Item>

            {transportVal === 'local' && (
              <Alert type="info" showIcon title={t.hosts.connLocal} />
            )}

            {transportVal === 'dsm_api' && (
              <>
                <Form.Item name="dsmUrl" label={t.hosts.dsmUrlLabel} rules={[{ required: true }]}>
                  <Input placeholder="http://192.168.1.10:5000" />
                </Form.Item>
                <Row gutter={12}>
                  <Col xs={24} sm={12}>
                    <Form.Item name="dsmUser" label={t.hosts.dsmUserLabel} rules={[{ required: true }]}>
                      <Input placeholder="admin" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={12}>
                    <Form.Item
                      name="dsmSecret"
                      label={t.hosts.dsmSecretLabel}
                      rules={[{ required: !editing }]}
                    >
                      <Input.Password />
                    </Form.Item>
                  </Col>
                </Row>
                {showOtpField || testResult?.needOtp ? (
                  <Form.Item name="dsmOtpCode" label={t.hosts.dsmOtpLabel} style={{ marginBottom: 4 }}>
                    <Input placeholder={t.hosts.dsmOtpPlaceholder} maxLength={8} />
                  </Form.Item>
                ) : (
                  <Button
                    type="link"
                    size="small"
                    style={{ padding: 0 }}
                    onClick={() => setShowOtpField(true)}
                  >
                    {t.hosts.dsmOtpToggle}
                  </Button>
                )}
              </>
            )}

            {transportVal === 'ssh' && (
              <>
                <Row gutter={12}>
                  <Col span={17}>
                    <Form.Item name="address" label={t.hosts.addressLabel} rules={[{ required: true }]}>
                      <Input placeholder="192.168.1.10" />
                    </Form.Item>
                  </Col>
                  <Col span={7}>
                    <Form.Item name="port" label={t.hosts.portLabel}>
                      <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                    </Form.Item>
                  </Col>
                </Row>
                <Row gutter={12}>
                  <Col xs={24} sm={12}>
                    <Form.Item name="sshUser" label={t.hosts.userLabel} rules={[{ required: true }]}>
                      <Input placeholder="root / admin" />
                    </Form.Item>
                  </Col>
                  <Col xs={24} sm={12}>
                    <Form.Item name="sshAuth" label={t.hosts.authTypeLabel}>
                      <Radio.Group>
                        <Radio value="password">{t.hosts.authPassword}</Radio>
                        <Radio value="key">{t.hosts.authKey}</Radio>
                      </Radio.Group>
                    </Form.Item>
                  </Col>
                </Row>
                {authVal === 'key' ? (
                  <>
                    <Space style={{ marginBottom: 8 }}>
                      <Button size="small" icon={<KeyOutlined />} onClick={handleGenerateKey}>
                        {t.hosts.generateKey}
                      </Button>
                    </Space>
                    {generatedPubKey && (
                      <Alert
                        type="info"
                        style={{ marginBottom: 12 }}
                        title={t.hosts.generatedPubTip}
                        description={
                          <Space direction="vertical" style={{ width: '100%' }}>
                            <Typography.Text code style={{ fontSize: 11, wordBreak: 'break-all' }}>
                              {generatedPubKey}
                            </Typography.Text>
                            <Button
                              size="small"
                              icon={<CopyOutlined />}
                              onClick={() => {
                                navigator.clipboard.writeText(generatedPubKey)
                                message.success(t.detail.copied)
                              }}
                            >
                              {t.detail.copyCmd}
                            </Button>
                          </Space>
                        }
                      />
                    )}
                    <Form.Item name="sshSecret" label={t.hosts.privateKeyLabel}>
                      <Input.TextArea rows={3} placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" />
                    </Form.Item>
                    <Form.Item name="sshPassphrase" label={t.hosts.passphraseLabel} style={{ marginBottom: 4 }}>
                      <Input.Password />
                    </Form.Item>
                  </>
                ) : (
                  <Form.Item name="sshSecret" label={t.hosts.passwordLabel} style={{ marginBottom: 4 }}>
                    <Input.Password />
                  </Form.Item>
                )}
              </>
            )}
          </Form>

          {/* 4. 内联测试诊断结果 */}
          {testResult && (
            <Card
              size="small"
              style={{
                background: token.colorFillAlter,
                borderRadius: 8,
              }}
            >
              <Space direction="vertical" size="small" style={{ width: '100%' }}>
                <Alert
                  type={testResult.ok ? 'success' : 'error'}
                  showIcon
                  title={testResult.ok ? t.hosts.testPassed : t.hosts.testFailed}
                />
                <Timeline
                  style={{ marginTop: 8, marginBottom: 0 }}
                  items={testResult.steps.map((s) => ({
                    color: s.status === 'ok' ? 'green' : 'red',
                    dot: s.status === 'ok' ? <CheckCircleOutlined /> : <CloseCircleOutlined />,
                    children: (
                      <div>
                        <Typography.Text strong>{s.step}</Typography.Text>
                        <div>
                          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                            {s.message}
                          </Typography.Text>
                        </div>
                      </div>
                    ),
                  }))}
                />
                {transportVal === 'ssh' && testResult.hostKeyChanged && (
                  <Alert
                    type="warning"
                    showIcon
                    icon={<WarningOutlined />}
                    title={t.hosts.fingerprintChanged}
                    description={
                      testResult.previousHostKey
                        ? `${t.hosts.fingerprintPrevious}: ${testResult.previousHostKey}`
                        : undefined
                    }
                  />
                )}
              </Space>
            </Card>
          )}
        </Space>
      </Modal>
    </Space>
  )
}
