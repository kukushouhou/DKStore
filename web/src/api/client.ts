// 统一的 API 请求封装。后端错误格式：{"error":{"code","message"}}
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
  const res = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
  const body = await res.json().catch(() => null)
  if (!res.ok) {
    const err = body?.error
    throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `请求失败（${res.status}）`)
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

export const api = {
  health: () => request<Health>('/health'),
}
