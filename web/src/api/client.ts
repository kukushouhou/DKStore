import { useUIStore } from '../store/ui'

export class ApiError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const locale = useUIStore.getState().locale
  const res = await fetch(`/api/v1${path}`, {
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
    throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `HTTP ${res.status}`)
  }
  return body as T
}

export interface Health {
  status: string
  version: string
  commit: string
  schemaVersion: number
  time: number
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
  transport: 'local' | 'ssh'
  address?: string
  port?: number
  sshUser?: string
  sshAuth?: 'password' | 'key'
  sshHostKey?: string
  transportMode?: string
  info?: HostInfo
  status: 'online' | 'offline' | 'unknown'
  lastSeenAt?: number
}

export interface HostInput {
  name: string
  kind: string
  transport: 'local' | 'ssh'
  address?: string
  port?: number
  sshUser?: string
  sshAuth?: 'password' | 'key'
  sshSecret?: string
  sshPassphrase?: string
  sshHostKey?: string
}

export interface HostTestResult {
  ok: boolean
  steps: { step: string; status: 'ok' | 'error' | 'skipped'; message: string }[]
  presentedHostKey?: string
  hostKeyTrusted: boolean
  transportMode?: string
  info?: HostInfo
}

export interface RepoLink {
  repo?: string
  releases?: string
}

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
  databaseName?: string
  reasonText?: string
  currentVersion?: string
  targetVersion?: string
  rawTargetVersion?: string
  targetRef?: string
  targetDigest?: string
  targetTags?: string[]
  hint?: string
  errorKind?: string
  errorMsg?: string
  links: RepoLink
  ignored: boolean
  ignoreMode?: 'this_version' | 'forever'
  checkedAt?: number
}

export interface AppCardItem {
  key: string
  hostId: string
  hostName: string
  hostStatus: string
  name: string
  source: 'standalone' | 'compose' | 'dsm_project' | 'vendor_managed'
  group: 'normal' | 'caution' | 'ignored' | 'up_to_date'
  summaryCurrent?: string
  summaryTarget?: string
  reasonText?: string
  isDatabase: boolean
  databaseName?: string
  links: RepoLink
  updateServiceKeys?: string[]
  ignoreMode?: 'this_version' | 'forever'
  services: ServiceItem[]
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
  completed: number
  total: number
  startedAt?: number
}

export interface OverviewResponse {
  hosts: HostItem[]
  normal: AppCardItem[]
  caution: AppCardItem[]
  ignored: AppCardItem[]
  upToDateCount: number
  totalAppCount: number
  failed?: FailedItem[]
  lastCheckedAt?: number
  progress: CheckProgress
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

export const api = {
  health: () => request<Health>('/health'),
  authStatus: () => request<{ passwordEnabled: boolean; authenticated: boolean }>('/auth/status'),
  login: (password: string) =>
    request<{ ok: boolean }>('/auth/login', { method: 'POST', body: JSON.stringify({ password }) }),
  logout: () => request<{ ok: boolean }>('/auth/logout', { method: 'POST' }),
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

  setIgnore: (serviceKeys: string[], mode: 'this_version' | 'forever') =>
    request<{ ok: boolean }>('/ignore', {
      method: 'POST',
      body: JSON.stringify({ serviceKeys, mode }),
    }),
  removeIgnoreByService: (serviceKey: string) =>
    request<{ ok: boolean }>(`/ignore?serviceKey=${encodeURIComponent(serviceKey)}`, { method: 'DELETE' }),
  removeIgnoreByApp: (appKey: string) =>
    request<{ ok: boolean }>(`/ignore?appKey=${encodeURIComponent(appKey)}`, { method: 'DELETE' }),

  listHosts: () => request<{ items: HostItem[] }>('/hosts'),
  createHost: (input: HostInput) =>
    request<HostItem>('/hosts', { method: 'POST', body: JSON.stringify(input) }),
  updateHost: (id: string, input: HostInput) =>
    request<HostItem>(`/hosts/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(input) }),
  deleteHost: (id: string) =>
    request<{ ok: boolean }>(`/hosts/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  testHost: (input: HostInput) =>
    request<HostTestResult>('/hosts/test', { method: 'POST', body: JSON.stringify(input) }),
  keygen: () => request<{ privateKey: string; authorizedKey: string }>('/hosts/keygen', { method: 'POST' }),

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
}
