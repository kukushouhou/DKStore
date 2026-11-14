import type {
  AppCardItem,
  AppRuntimeTree,
  ContainerRuntimeStat,
  LocalMeta,
  ServiceItem,
  ServicePortItem,
} from '../api/client'

export const USER_TAG_COLORS = ['blue', 'cyan', 'purple', 'geekblue', 'magenta', 'orange'] as const

export function pickUserTagColor(tag: string) {
  let h = 0
  for (let i = 0; i < tag.length; i++) {
    h = (h * 31 + tag.charCodeAt(i)) | 0
  }
  return USER_TAG_COLORS[Math.abs(h) % USER_TAG_COLORS.length]
}

export function effectiveHostAddress(hostAddress?: string): string {
  const trimmed = (hostAddress || '').trim()
  if (trimmed) return trimmed
  if (typeof window !== 'undefined' && window.location.hostname) {
    return window.location.hostname
  }
  return 'localhost'
}

function formatUrlHost(host: string): string {
  const clean = host.trim().replace(/^\[|\]$/g, '')
  if (clean.includes(':')) {
    return `[${clean}]`
  }
  return clean
}

export function defaultSchemeForPort(
  hostPort: string,
  containerPort?: string,
): 'http' | 'https' {
  if (hostPort === '443' || hostPort === '8443' || containerPort === '443') {
    return 'https'
  }
  return 'http'
}

function normalizeCustomUrl(raw?: string): string | null {
  const u = (raw || '').trim()
  if (!u) return null
  if (/^https?:\/\//i.test(u)) return u
  return `http://${u}`
}

function isUsableSpecificHostIp(ip?: string): boolean {
  const s = (ip || '').trim().replace(/^\[|\]$/g, '')
  if (!s || s === '0.0.0.0' || s === '::' || s === '*' || s === '127.0.0.1' || s === '::1') {
    return false
  }
  return true
}

export function buildPortUrl(
  hostAddress: string | undefined,
  port: ServicePortItem,
  schemeOverride?: string,
): string {
  const host = isUsableSpecificHostIp(port.hostIp)
    ? port.hostIp!.trim()
    : effectiveHostAddress(hostAddress)
  const scheme =
    schemeOverride === 'http' || schemeOverride === 'https'
      ? schemeOverride
      : defaultSchemeForPort(port.hostPort, port.containerPort)
  return `${scheme}://${formatUrlHost(host)}:${port.hostPort}`
}

export function resolveContainerUrl(
  hostAddress: string | undefined,
  c: { state?: string; ports?: ServicePortItem[]; meta?: LocalMeta },
): string | null {
  const mode = c.meta?.accessMode || 'auto'
  if (mode === 'none') return null
  if (mode === 'custom') return normalizeCustomUrl(c.meta?.customUrl)
  if (c.state && c.state !== 'running') return null
  const ports = c.ports ?? []
  if (ports.length === 0) return null
  const picked =
    (c.meta?.fixedPort ? ports.find((p) => p.hostPort === c.meta?.fixedPort) : undefined) ??
    ports[0]
  return buildPortUrl(hostAddress, picked, c.meta?.scheme)
}

export interface PortCandidate {
  port: ServicePortItem
  ownerName: string
}

function collectPortCandidates(
  items: {
    name: string
    state?: string
    ports?: ServicePortItem[]
    meta?: LocalMeta
  }[],
  requireRunning: boolean,
): PortCandidate[] {
  const seen = new Set<string>()
  const out: PortCandidate[] = []
  for (const it of items) {
    if (it.meta?.accessMode === 'none') continue
    if (requireRunning && it.state && it.state !== 'running') continue
    for (const p of it.ports ?? []) {
      if (!p.hostPort || seen.has(p.hostPort)) continue
      seen.add(p.hostPort)
      out.push({ port: p, ownerName: it.name })
    }
  }
  return out
}

export function resolveTreeAccessUrl(tree: AppRuntimeTree): string | null {
  if (tree.containers.length === 1) {
    const c = tree.containers[0]
    const mergedMeta = tree.meta ?? c.meta
    return resolveContainerUrl(tree.hostAddress, {
      state: c.state,
      ports: c.ports,
      meta: mergedMeta,
    })
  }
  const mode = tree.meta?.accessMode || 'auto'
  if (mode === 'none') return null
  if (mode === 'custom') return normalizeCustomUrl(tree.meta?.customUrl)

  const candidates = collectPortCandidates(tree.containers, true)
  if (tree.meta?.fixedPort) {
    const matched = candidates.find((c) => c.port.hostPort === tree.meta?.fixedPort)
    if (matched) {
      return buildPortUrl(tree.hostAddress, matched.port, tree.meta.scheme)
    }
  }
  for (const c of tree.containers) {
    if (c.state === 'running' && c.meta?.accessMode === 'custom') {
      const cu = normalizeCustomUrl(c.meta.customUrl)
      if (cu) return cu
    }
  }
  if (candidates.length === 0) return null
  return buildPortUrl(tree.hostAddress, candidates[0].port, tree.meta?.scheme)
}

export function resolveHomeCardUrl(app: AppCardItem): string | null {
  const pool = app.allServices?.length ? app.allServices : app.services
  const totalCount = app.totalServiceCount ?? pool.length
  const isSplitSingleSvc = totalCount > 1 && app.services.length === 1

  if (isSplitSingleSvc) {
    const svc = app.services[0]
    if (svc.meta?.accessMode === 'none') return null
    if (svc.meta?.accessMode === 'custom') return normalizeCustomUrl(svc.meta.customUrl)
    if ((svc.ports?.length ?? 0) > 0) {
      if (app.meta?.accessMode === 'none') return null
      if (app.meta?.accessMode === 'custom') {
        const cu = normalizeCustomUrl(app.meta.customUrl)
        if (cu) return cu
      }
      return resolveContainerUrl(app.hostAddress, {
        state: svc.state,
        ports: svc.ports,
        meta: svc.meta ?? app.meta,
      })
    }
    if (svc.isDatabase || svc.roleKind === 'database' || svc.roleKind === 'mq' || svc.roleKind === 'middleware') {
      return null
    }
  }

  const mode = app.meta?.accessMode || 'auto'
  if (mode === 'none') return null
  if (mode === 'custom') return normalizeCustomUrl(app.meta?.customUrl)

  const candidates = collectPortCandidates(pool, true)
  if (app.meta?.fixedPort) {
    const matched = candidates.find((c) => c.port.hostPort === app.meta?.fixedPort)
    if (matched) {
      return buildPortUrl(app.hostAddress, matched.port, app.meta.scheme)
    }
  }
  for (const s of pool) {
    if ((!s.state || s.state === 'running') && s.meta?.accessMode === 'custom') {
      const cu = normalizeCustomUrl(s.meta.customUrl)
      if (cu) return cu
    }
  }
  if (candidates.length === 0) return null
  return buildPortUrl(app.hostAddress, candidates[0].port, app.meta?.scheme)
}

export function getHomeCardDisplayInfo(
  app: AppCardItem,
  projectSubPrefix: string,
  servicesCountTpl: string,
): { title: string; subtitle: string; tags: string[] } {
  const pool = app.allServices?.length ? app.allServices : app.services
  const totalCount = app.totalServiceCount ?? pool.length
  const isMulti = totalCount >= 2
  const isSplitSingleSvc = isMulti && app.services.length === 1
  const rawProjName = app.projectName || app.appName || app.name

  const projAlias = (app.meta?.alias || '').trim()
  const svc = isSplitSingleSvc ? app.services[0] : null
  const svcAlias = (svc?.meta?.alias || '').trim()

  let title = app.name
  const subParts: string[] = [app.hostName]

  if (!isMulti) {
    if (projAlias) {
      title = projAlias
      if (projAlias !== app.name) subParts.push(app.name)
    }
  } else if (isSplitSingleSvc && svc) {
    if (svcAlias) {
      title = svcAlias
      subParts.push(`${projectSubPrefix}${projAlias || rawProjName} (${svc.name})`)
    } else if (projAlias) {
      title = app.name !== rawProjName ? `${projAlias} · ${app.name}` : projAlias
      subParts.push(`${projectSubPrefix}${rawProjName}`)
    } else if (rawProjName && rawProjName !== app.name) {
      subParts.push(`${projectSubPrefix}${rawProjName}`)
    } else {
      subParts.push(servicesCountTpl.replace('{count}', String(totalCount)))
    }
  } else {
    if (projAlias) {
      title = projAlias
      if (projAlias !== rawProjName) subParts.push(rawProjName)
    }
    subParts.push(servicesCountTpl.replace('{count}', String(totalCount)))
  }

  const tagSet = new Set<string>()
  for (const t of app.meta?.tags ?? []) {
    if (t.trim()) tagSet.add(t.trim())
  }
  if (isSplitSingleSvc && svc?.meta?.tags) {
    for (const t of svc.meta.tags) {
      if (t.trim()) tagSet.add(t.trim())
    }
  }
  return {
    title,
    subtitle: subParts.join(' · '),
    tags: Array.from(tagSet),
  }
}

export interface MetaScopeOption {
  scopeId: string
  targetKey: string
  label: string
  rawName: string
  iconUrl?: string
  isProject: boolean
  ports: PortCandidate[]
  initialMeta?: LocalMeta
}

export interface MetaModalContext {
  hostId: string
  hostName: string
  hostAddress?: string
  defaultScopeId: string
  scopes: MetaScopeOption[]
}

export function buildMetaContextFromTree(
  tree: AppRuntimeTree,
  targetContainer?: ContainerRuntimeStat,
  wholeProjectLabel = '整个项目',
): MetaModalContext {
  const isMulti = tree.containers.length > 1
  if (!isMulti) {
    const c = tree.containers[0]
    const ports: PortCandidate[] = (c?.ports ?? []).map((p) => ({
      port: p,
      ownerName: c.name,
    }))
    return {
      hostId: tree.hostId,
      hostName: tree.hostName,
      hostAddress: tree.hostAddress,
      defaultScopeId: 'project',
      scopes: [
        {
          scopeId: 'project',
          targetKey: tree.key,
          label: tree.name,
          rawName: tree.name,
          iconUrl: tree.iconUrl || c?.iconUrl,
          isProject: false,
          ports,
          initialMeta: tree.meta ?? c?.meta,
        },
      ],
    }
  }

  const projPorts = collectPortCandidates(tree.containers, false)
  const scopes: MetaScopeOption[] = [
    {
      scopeId: 'project',
      targetKey: tree.key,
      label: wholeProjectLabel,
      rawName: tree.name,
      iconUrl: tree.iconUrl,
      isProject: true,
      ports: projPorts,
      initialMeta: tree.meta,
    },
    ...tree.containers.map((c) => {
      const mKey = c.metaKey || `${tree.key}/ctr/${c.name}`
      const scopeId = mKey
      return {
        scopeId,
        targetKey: mKey,
        label: c.service || c.name,
        rawName: c.name,
        iconUrl: c.iconUrl,
        isProject: false,
        ports: (c.ports ?? []).map((p) => ({ port: p, ownerName: c.name })),
        initialMeta: c.meta,
      }
    }),
  ]

  let defaultScopeId = 'project'
  if (targetContainer) {
    const tKey = targetContainer.metaKey || `${tree.key}/ctr/${targetContainer.name}`
    if (scopes.some((s) => s.scopeId === tKey)) {
      defaultScopeId = tKey
    }
  }

  return {
    hostId: tree.hostId,
    hostName: tree.hostName,
    hostAddress: tree.hostAddress,
    defaultScopeId,
    scopes,
  }
}

export function buildMetaContextFromCard(
  app: AppCardItem,
  wholeProjectLabel = '整个项目',
): MetaModalContext {
  const pool: ServiceItem[] = app.allServices?.length ? app.allServices : app.services
  const totalCount = app.totalServiceCount ?? pool.length
  const isMulti = totalCount >= 2 && pool.length >= 2
  const rawProjName = app.projectName || app.appName || app.name

  if (!isMulti) {
    const s = pool[0]
    return {
      hostId: app.hostId,
      hostName: app.hostName,
      hostAddress: app.hostAddress,
      defaultScopeId: 'project',
      scopes: [
        {
          scopeId: 'project',
          targetKey: app.key,
          label: rawProjName,
          rawName: rawProjName,
          iconUrl: app.iconUrl || s?.iconUrl,
          isProject: false,
          ports: (s?.ports ?? []).map((p) => ({ port: p, ownerName: s.name })),
          initialMeta: app.meta ?? s?.meta,
        },
      ],
    }
  }

  const projPorts = collectPortCandidates(pool, false)
  const scopes: MetaScopeOption[] = [
    {
      scopeId: 'project',
      targetKey: app.key,
      label: wholeProjectLabel,
      rawName: rawProjName,
      iconUrl: app.iconUrl,
      isProject: true,
      ports: projPorts,
      initialMeta: app.meta,
    },
    ...pool.map((s) => {
      const mKey = s.metaKey || `${app.key}/svc/${s.name}`
      return {
        scopeId: mKey,
        targetKey: mKey,
        label: s.name,
        rawName: s.name,
        iconUrl: s.iconUrl,
        isProject: false,
        ports: (s.ports ?? []).map((p) => ({ port: p, ownerName: s.name })),
        initialMeta: s.meta,
      }
    }),
  ]

  let defaultScopeId = 'project'
  if (app.services.length === 1 && app.name !== rawProjName) {
    const s = app.services[0]
    const mKey = s.metaKey || `${app.key}/svc/${s.name}`
    if (scopes.some((sc) => sc.scopeId === mKey)) {
      defaultScopeId = mKey
    }
  }

  return {
    hostId: app.hostId,
    hostName: app.hostName,
    hostAddress: app.hostAddress,
    defaultScopeId,
    scopes,
  }
}
