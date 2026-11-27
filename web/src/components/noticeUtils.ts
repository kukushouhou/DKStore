import type { HostItem } from '../api/client'

export function formatHostEndpoint(h: HostItem, localLabel: string): string {
  if (h.transport === 'dsm_api') {
    const clean = (h.dsmUrl || h.address || '').replace(/^https?:\/\//i, '').replace(/\/+$/, '')
    return clean || h.name
  }
  if (h.transport === 'ssh') {
    return h.address ? `${h.address}:${h.port || 22}` : h.name
  }
  return localLabel
}

export function formatShortNames(names: string[], sep: string, moreTpl: string): string {
  const uniq: string[] = []
  const seen = new Set<string>()
  for (const raw of names) {
    const n = raw.trim()
    if (n && !seen.has(n)) {
      seen.add(n)
      uniq.push(n)
    }
  }
  if (uniq.length <= 2) {
    return uniq.join(sep)
  }
  return moreTpl.replace('{names}', uniq.slice(0, 2).join(sep))
}
