import { useUIStore } from '../store/ui'

export class ApiError extends Error {
  status: number
  code: string
  testResult?: HostTestResult
  constructor(status: number, code: string, message: string, testResult?: HostTestResult) {
    super(message)
    this.status = status
    this.code = code
    this.testResult = testResult
  }
}

export function gatewayPrefix(): string {
  if (typeof window === 'undefined') return ''
  const p = window.location.pathname
  if (p === '/app/dkstore' || p.startsWith('/app/dkstore/')) {
    return '/app/dkstore'
  }
  if (p === '/dkstore' || p.startsWith('/dkstore/')) {
    return '/dkstore'
  }
  return ''
}

export function apiBase(): string {
  return `${gatewayPrefix()}/api/v1`
}

export function resolveIconUrl(iconUrl?: string): string | undefined {
  if (!iconUrl) return undefined
  if (
    iconUrl.startsWith('http://') ||
    iconUrl.startsWith('https://') ||
    iconUrl.startsWith('data:')
  ) {
    return iconUrl
  }
  const prefix = gatewayPrefix()
  if (iconUrl.startsWith('/app/dkstore/api/v1') || iconUrl.startsWith('/dkstore/api/v1')) {
    return iconUrl
  }
  if (iconUrl.startsWith('/api/v1')) {
    return prefix ? `${prefix}${iconUrl}` : iconUrl
  }
  if (iconUrl.startsWith('/')) {
    return `${apiBase()}${iconUrl}`
  }
  return `${apiBase()}/${iconUrl}`
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const locale = useUIStore.getState().locale
  const res = await fetch(`${apiBase()}${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: {
      'Content-Type': 'application/json',
      'X-DKStore-Lang': locale,
      'Accept-Language': locale,
      ...init?.headers,
    },
  })
  const body = await res.json().catch(() => null)
  if (!res.ok) {
    const err = body?.error
    throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `HTTP ${res.status}`, body?.testResult)
  }
  return body as T
}

/** 访问模式：device=宿主设备管理员登录态；password=DKStore 自有密码；setup_required=必须先设置密码；host_login_required=套件模式未登录宿主后台。 */
export type AuthMode = 'device' | 'password' | 'setup_required' | 'host_login_required'

export interface AuthStatus {
  mode: AuthMode
  authenticated: boolean
  installMode: string
  /** 宿主登录态核验失败原因（已本地化），仅群晖/OpenWrt 套件安装时可能给出。 */
  deviceAuthHint?: string
  hasLocalHost?: boolean
  canAddLocal?: boolean
  localStatus?: 'ok' | 'socket_missing' | 'permission_denied' | string
}

export interface HostsListResponse {
  items: HostItem[]
  hasLocalHost?: boolean
  canAddLocal?: boolean
  localStatus?: string
}

export interface ContainerRuntimeStat {
  id: string
  name: string
  service?: string
  image: string
  state: string
  health?: string
  uptime?: string
  startedAt?: number
  cpuPercent: number
  memUsage: number
  memLimit: number
}

export interface AppRuntimeTree {
  key: string
  hostId: string
  hostName: string
  name: string
  source: 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
  runningCount: number
  totalCount: number
  cpuPercent: number
  memUsage: number
  statsReady?: boolean
  containers: ContainerRuntimeStat[]
}

export interface HostInfo {
  os: string
  arch: string
  variant?: string
  dockerVersion: string
  platformVersion?: string
}

export interface HostItem {
  id: string
  name: string
  kind: string
  transport: 'local' | 'ssh' | 'dsm_api'
  address?: string
  port?: number
  sshUser?: string
  sshAuth?: 'password' | 'key'
  sshHostKey?: string
  /** 指纹曾自动更新时的旧指纹（可能重装过系统） */
  sshPrevHostKey?: string
  /** 指纹自动更新的时间（Unix 秒），非空时卡片提示指纹已变化 */
  sshHostKeyChangedAt?: number
  dsmUrl?: string
  dsmUser?: string
  transportMode?: string
  info?: HostInfo
  status: 'online' | 'offline' | 'unknown'
  lastSeenAt?: number
}

export interface HostInput {
  name: string
  kind: string
  transport: 'local' | 'ssh' | 'dsm_api'
  address?: string
  port?: number
  sshUser?: string
  sshAuth?: 'password' | 'key'
  sshSecret?: string
  sshPassphrase?: string
  dsmUrl?: string
  dsmUser?: string
  dsmSecret?: string
  dsmOtpCode?: string
  /** 测试连接返回的 DSM 设备令牌暂存凭证，保存时带回以避免同一动态码被用两次 */
  deviceTokenNonce?: string
  /** 仅测试连接使用：编辑已有设备时复用其已保存的密码 / 私钥 */
  hostId?: string
}

export interface HostTestResult {
  ok: boolean
  needOtp?: boolean
  steps: { step: string; status: 'ok' | 'error' | 'skipped'; message: string }[]
  presentedHostKey?: string
  hostKeyChanged?: boolean
  previousHostKey?: string
  deviceTokenNonce?: string
  transportMode?: string
  info?: HostInfo
}

export interface RepoLink {
  repo?: string
  releases?: string
}

export type IgnoreMode = 'this_version' | 'prefix' | 'forever'
export type UpdatePolicyMode = 'all' | 'skip_latest' | 'prefix' | 'forever'

export interface ServiceItem {
  key: string
  name: string
  state: string
  imageRef: string
  repoDigest?: string
  currentTags?: string[]
  kind: 'none' | 'digest_update' | 'version_update' | 'check_failed' | 'uncomparable'
  category?: 'normal' | 'caution'
  reason?: string
  isDatabase?: boolean
  databaseName?: string
  roleKind?: 'database' | 'mq' | 'middleware' | string
  reasonText?: string
  currentVersion?: string
  targetVersion?: string
  rawTargetVersion?: string
  targetRef?: string
  targetDigest?: string
  targetTags?: string[]
  currentUpdatedAt?: number
  targetUpdatedAt?: number
  hint?: string
  errorKind?: string
  errorMsg?: string
  links: RepoLink
  ignored: boolean
  ignoreMode?: IgnoreMode
  ignorePrefix?: string
  ignoreTargetTag?: string
  checkedAt?: number
}

export interface AppCardItem {
  key: string
  hostId: string
  hostName: string
  hostStatus: string
  appName?: string
  name: string
  projectName?: string
  totalServiceCount?: number
  iconUrl?: string
  source: 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
  group: 'normal' | 'caution' | 'no_update' | 'ignored' | 'up_to_date'
  summaryCurrent?: string
  summaryTarget?: string
  reasonText?: string
  isDatabase: boolean
  databaseName?: string
  roleKind?: 'database' | 'mq' | 'middleware' | string
  ignored?: boolean
  ignoreMode?: IgnoreMode
  ignorePrefix?: string
  ignoreTargetTag?: string
  composeEditable?: boolean
  links: RepoLink
  updateServiceKeys?: string[]
  services: ServiceItem[]
  allServices?: ServiceItem[]
}

export interface FailedItem {
  serviceKey: string
  appKey: string
  appName: string
  serviceName: string
  hostName: string
  imageRef: string
  errorKind: string
  errorMsg: string
}

export interface CheckProgress {
  running: boolean
  runId?: number
  phase?: 'idle' | 'discovering' | 'checking' | 'done'
  hostName?: string
  currentServiceKey?: string
  currentAppName?: string
  completedServiceKeys?: string[]
  completed: number
  total: number
  startedAt?: number
}

export interface OverviewResponse {
  hosts: HostItem[]
  normal: AppCardItem[]
  caution: AppCardItem[]
  noUpdate?: AppCardItem[]
  ignored?: AppCardItem[]
  upToDateCount: number
  totalAppCount: number
  failed?: FailedItem[]
  lastCheckedAt?: number
  progress: CheckProgress
}

export interface ServiceTagItem {
  tag: string
  updatedAt?: number
  digest?: string
}

export interface ServiceTagsResponse {
  items: ServiceTagItem[]
  highestTag?: string
}

export interface SetIgnorePayload {
  serviceKeys: string[]
  mode: IgnoreMode
  prefix?: string
  targetTag?: string
  targetDigest?: string
}

export interface StartUpdateItemInput {
  serviceKey: string
  targetTag?: string
  policyMode?: UpdatePolicyMode
  policyPrefix?: string
  skipTargetTag?: string
}

export interface StartUpdatesPayload {
  serviceKeys?: string[]
  items?: StartUpdateItemInput[]
}

export interface AppComposeResponse {
  editable: boolean
  content: string
  reason?: string
}

export interface UpdateAppComposePayload {
  appKey: string
  content: string
  serviceKey?: string
  policyMode?: string
  policyPrefix?: string
  skipTargetTag?: string
}

export interface DeleteFileItem {
  id: string
  kind: 'project_dir' | 'volume' | 'bind_mount'
  path: string
  target?: string
  defaultSelect: boolean
  protected: boolean
  reason?: string
}

export interface DeleteAppPreviewResponse {
  appKey: string
  appName: string
  source: 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
  isProject: boolean
  containers: string[]
  images: string[]
  files: DeleteFileItem[]
}

export interface DeleteAppPayload {
  appKey: string
  deleteImages: boolean
  fileIds: string[]
}

export interface DeleteAppResult {
  removedContainers: number
  removedImages: number
  skippedImages?: string[]
  removedFiles: number
  warnings?: string[]
}

export interface ServiceEnvItem {
  key: string
  value: string
}

export interface ServicePortItem {
  hostIp?: string
  hostPort: string
  containerPort: string
  protocol: string
}

export interface ServiceVolumeItem {
  hostPath: string
  containerPath: string
  mode?: string
}

export interface ServiceConfigResponse {
  editable: boolean
  reason?: string
  imageRef: string
  env: ServiceEnvItem[]
  ports: ServicePortItem[]
  volumes?: ServiceVolumeItem[]
}

export interface UpdateServiceConfigPayload {
  serviceKey: string
  targetTag?: string
  policyMode?: string
  policyPrefix?: string
  skipTargetTag?: string
  env: ServiceEnvItem[]
  ports: ServicePortItem[]
  volumes?: ServiceVolumeItem[]
}

export interface CheckRunItem {
  id: number
  trigger: string
  startedAt: number
  finishedAt: number
  status: string
  summary: { total?: number; normal?: number; caution?: number; failed?: number }
  changes?: {
    change: 'appeared' | 'changed' | 'resolved'
    hostName: string
    appName: string
    serviceName: string
    imageRef: string
    currentVersion?: string
    targetVersion?: string
  }[]
}

export interface AppSettings {
  locale: 'zh-CN' | 'en-US'
  progressStyle?: 'layered' | 'unified'
  checkIntervalHours: number
  channel: string
  webhookUrl: string
  telegramBotToken: string
  telegramChatId: string
  quietHoursStart?: string
  quietHoursEnd?: string
}

export interface RegistryCred {
  registry: string
  username: string
  secret?: string
}

export interface LayerProgress {
  id: string
  status: 'waiting' | 'downloading' | 'extracting' | 'done' | string
  current: number
  total: number
  percent: number
}

export interface PullProgress {
  phase: 'precheck' | 'pulling' | 'recreating' | 'verifying' | 'done' | string
  statusText: string
  totalLayers: number
  completedLayers: number
  downloadedBytes: number
  totalBytes: number
  percent: number
  layers?: LayerProgress[]
  logChunk?: string
}

export interface UpdateJobItem {
  id: string
  kind: 'upgrade' | 'rollback'
  hostId: string
  hostName: string
  appKey: string
  appName: string
  serviceKey: string
  serviceName: string
  source: string
  status: 'queued' | 'running' | 'succeeded' | 'failed'
  phase: string
  progress: PullProgress
  oldImageRef: string
  oldImageId?: string
  oldRepoDigest?: string
  oldVersion?: string
  oldMatchedTags?: string[]
  targetImageRef: string
  targetRepoDigest?: string
  targetVersion?: string
  targetMatchedTags?: string[]
  newImageId?: string
  newRepoDigest?: string
  rollbackEligible: boolean
  rollbackRef?: string
  rollbackReason?: string
  rollbackReasonText?: string
  errorCode?: string
  errorMsg?: string
  logChunk?: string
  logOffset?: number
  logs?: string
  startedAt: number
  finishedAt?: number
}

export interface RollbackVerifyResult {
  eligible: boolean
  rollbackRef?: string
  oldVersion?: string
  oldDigest?: string
  reasonCode?: string
  reasonText?: string
  dataWarning?: string
}

export interface SelfUpdateStatus {
  currentVersion: string
  commit: string
  latestVersion: string
  hasUpdate: boolean
  installMode: string
  releaseUrl: string
  preserveNote: string
  commandHint: string
}

export const api = {
  authStatus: () => request<AuthStatus>('/auth/status'),
  login: (password: string) =>
    request<{ ok: boolean }>('/auth/login', { method: 'POST', body: JSON.stringify({ password }) }),
  logout: () => request<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
  /** 首次设置管理员密码（仅未设置密码时可用），成功后直接登录。 */
  setupPassword: (password: string) =>
    request<{ ok: boolean }>('/auth/setup', { method: 'POST', body: JSON.stringify({ password }) }),
  /** 修改管理员密码（必须已设置密码并提供旧密码）。 */
  setPassword: (oldPassword: string, newPassword: string) =>
    request<{ ok: boolean }>('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ oldPassword, newPassword }),
    }),

  overview: (hostId?: string) =>
    request<OverviewResponse>(hostId ? `/overview?hostId=${encodeURIComponent(hostId)}` : '/overview'),
  triggerCheck: (hostId?: string, bypassCache = true) =>
    request<CheckProgress>('/check', {
      method: 'POST',
      body: JSON.stringify({ hostId: hostId || '', bypassCache }),
    }),
  checkRuns: () => request<{ items: CheckRunItem[] }>('/check-runs'),

  setIgnore: (payload: SetIgnorePayload) =>
    request<{ ok: boolean }>('/ignore', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  removeIgnoreByService: (serviceKey: string) =>
    request<{ ok: boolean }>(`/ignore?serviceKey=${encodeURIComponent(serviceKey)}`, { method: 'DELETE' }),
  removeIgnoreByApp: (appKey: string) =>
    request<{ ok: boolean }>(`/ignore?appKey=${encodeURIComponent(appKey)}`, { method: 'DELETE' }),

  searchServiceTags: (serviceKey: string, q = '', remote = false) =>
    request<ServiceTagsResponse>(
      `/services/tags?serviceKey=${encodeURIComponent(serviceKey)}&q=${encodeURIComponent(q)}&remote=${remote ? 1 : 0}`,
    ),

  getAppCompose: (appKey: string) =>
    request<AppComposeResponse>(`/apps/compose?key=${encodeURIComponent(appKey)}`),
  updateAppCompose: (payload: UpdateAppComposePayload) =>
    request<{ ok: boolean; job?: UpdateJobItem; items?: UpdateJobItem[] }>('/apps/compose-update', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  previewDeleteApp: (appKey: string) =>
    request<DeleteAppPreviewResponse>(`/apps/delete-preview?key=${encodeURIComponent(appKey)}`),
  deleteApp: (payload: DeleteAppPayload) =>
    request<{ ok: boolean; result: DeleteAppResult }>('/apps/delete', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  getServiceConfig: (serviceKey: string) =>
    request<ServiceConfigResponse>(`/services/config?serviceKey=${encodeURIComponent(serviceKey)}`),
  updateServiceConfig: (payload: UpdateServiceConfigPayload) =>
    request<{ ok: boolean; job?: UpdateJobItem }>('/services/config-update', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  listHosts: () => request<HostsListResponse>('/hosts'),
  createHost: (input: HostInput) =>
    request<HostItem>('/hosts', { method: 'POST', body: JSON.stringify(input) }),
  updateHost: (id: string, input: HostInput) =>
    request<HostItem>(`/hosts/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(input) }),
  deleteHost: (id: string) =>
    request<{ ok: boolean }>(`/hosts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testHost: (input: HostInput) =>
    request<HostTestResult>('/hosts/test', { method: 'POST', body: JSON.stringify(input) }),
  keygen: () => request<{ privateKey: string; authorizedKey: string }>('/hosts/keygen', { method: 'POST' }),

  listContainers: (
    hostId?: string,
    opts?: boolean | { withStats?: boolean; cached?: boolean },
  ) => {
    const params = new URLSearchParams()
    if (hostId) params.set('hostId', hostId)
    const withStats = typeof opts === 'boolean' ? opts : (opts?.withStats ?? true)
    const cached = typeof opts === 'object' && Boolean(opts?.cached)
    if (cached) {
      params.set('cached', '1')
    } else if (!withStats) {
      params.set('stats', '0')
    }
    const qs = params.toString()
    return request<{ items: AppRuntimeTree[]; cached?: boolean }>(
      qs ? `/containers?${qs}` : '/containers',
    )
  },

  getSettings: () => request<AppSettings>('/settings'),
  saveSettings: (cfg: AppSettings) =>
    request<AppSettings>('/settings', { method: 'PUT', body: JSON.stringify(cfg) }),
  testNotify: (cfg: AppSettings) =>
    request<{ ok: boolean }>('/notify/test', { method: 'POST', body: JSON.stringify(cfg) }),

  listRegCreds: () => request<{ items: RegistryCred[] }>('/registry-credentials'),
  saveRegCred: (cred: RegistryCred) =>
    request<{ ok: boolean }>('/registry-credentials', { method: 'POST', body: JSON.stringify(cred) }),
  deleteRegCred: (registry: string) =>
    request<{ ok: boolean }>(`/registry-credentials?registry=${encodeURIComponent(registry)}`, {
      method: 'DELETE',
    }),

  listUpdates: () => request<{ items: UpdateJobItem[] }>('/updates'),
  getUpdateJob: (id: string) =>
    request<UpdateJobItem>(`/updates/${encodeURIComponent(id)}`),
  startUpdates: (payload: string[] | StartUpdatesPayload) =>
    request<{ items: UpdateJobItem[] }>('/updates', {
      method: 'POST',
      body: JSON.stringify(Array.isArray(payload) ? { serviceKeys: payload } : payload),
    }),
  verifyRollback: (id: string) =>
    request<RollbackVerifyResult>(`/updates/${encodeURIComponent(id)}/rollback-check`),
  executeRollback: (id: string) =>
    request<UpdateJobItem>(`/updates/${encodeURIComponent(id)}/rollback`, { method: 'POST' }),
  selfUpdateStatus: () => request<SelfUpdateStatus>('/self-update/status'),
}
