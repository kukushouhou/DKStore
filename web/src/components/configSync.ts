import type {
  ServiceConfigResponse,
  ServiceDeviceItem,
  ServiceEnvItem,
  ServicePortItem,
  ServiceVolumeItem,
} from '../api/client'

export interface EditableServiceConfig {
  name: string
  image: string
  networkMode: string // 'bridge' | 'host' | 'none' | custom network
  networkSource?: 'network_mode' | 'networks'
  restartPolicy: string // '' | 'no' | 'always' | 'unless-stopped' | 'on-failure'
  user: string
  privileged: boolean
  capAdd: string[]
  hostname: string
  extraHosts: string[]
  extraFlags: string[]
  cmdArgs: string[]
  envStyle?: 'list' | 'map'
  envs: ServiceEnvItem[]
  ports: ServicePortItem[]
  volumes: ServiceVolumeItem[]
  devices: ServiceDeviceItem[]
}

export interface DiffChangedItem {
  serviceName: string
  label: string
  from: string
  to: string
}

export interface SectionDiff {
  changed: DiffChangedItem[]
  added: string[]
  removed: string[]
}

export interface ServiceConfigDiff {
  runtime: DiffChangedItem[]
  envs: SectionDiff
  ports: SectionDiff
  volumes: SectionDiff
  devices: SectionDiff
}

export interface ConfigDiffSummary {
  byService: Record<string, ServiceConfigDiff>
  changed: DiffChangedItem[]
  added: string[]
  removed: string[]
}

export interface ConfigDiffI18n {
  diffNetLabel: string
  diffRestartLabel: string
  diffUserLabel: string
  diffUserDefault: string
  diffPrivLabel: string
  diffPrivOn: string
  diffPrivOff: string
  diffEnvPrefix: string
  diffPortPrefix: string
  diffVolPrefix: string
  diffDevPrefix: string
  emptyVal: string
}

export function normalizeNetMode(raw?: string): string {
  const s = (raw ?? '').trim()
  if (!s || s === 'default') return 'bridge'
  return s
}

export function normalizeRestart(raw?: string): string {
  const s = (raw ?? '').trim()
  if (!s || s === 'no') return ''
  return s
}

export function cloneEditableConfig(cfg: EditableServiceConfig): EditableServiceConfig {
  return {
    ...cfg,
    capAdd: [...cfg.capAdd],
    extraHosts: [...cfg.extraHosts],
    extraFlags: [...cfg.extraFlags],
    cmdArgs: [...cfg.cmdArgs],
    envs: cfg.envs.map((e) => ({ ...e })),
    ports: cfg.ports.map((p) => ({ ...p })),
    volumes: cfg.volumes.map((v) => ({ ...v })),
    devices: cfg.devices.map((d) => ({ ...d })),
  }
}

export function editableConfigFromApi(
  res: ServiceConfigResponse,
  fallbackName: string,
  targetImageRef: string,
): EditableServiceConfig {
  return {
    name: res.containerName || fallbackName,
    image: targetImageRef || res.imageRef,
    networkMode: normalizeNetMode(res.networkMode),
    restartPolicy: normalizeRestart(res.restartPolicy),
    user: (res.user ?? '').trim(),
    privileged: Boolean(res.privileged),
    capAdd: [...(res.capAdd ?? [])],
    hostname: (res.hostname ?? '').trim(),
    extraHosts: [...(res.extraHosts ?? [])],
    extraFlags: [],
    cmdArgs: [],
    envs: (res.env ?? []).map((e) => ({ key: e.key, value: e.value })),
    ports: (res.ports ?? []).map((p) => ({
      ...(p.hostIp ? { hostIp: p.hostIp } : {}),
      hostPort: p.hostPort,
      containerPort: p.containerPort,
      protocol: (p.protocol || 'tcp').toLowerCase(),
    })),
    volumes: (res.volumes ?? []).map((v) => ({
      hostPath: v.hostPath,
      containerPath: v.containerPath,
      mode: v.mode || 'rw',
    })),
    devices: (res.devices ?? []).map((d) => ({
      hostPath: d.hostPath,
      containerPath: d.containerPath || d.hostPath,
      permissions: d.permissions,
    })),
  }
}

function shellQuote(val: string): string {
  if (!val) return '""'
  if (/[\s"'\\$`]/.test(val)) {
    return `"${val.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }
  return val
}

/** 根据结构化配置生成格式整洁的 docker run 多行命令 */
export function buildDockerRunCommand(cfg: EditableServiceConfig): string {
  const parts: string[] = ['docker run -d']
  if (cfg.name.trim()) {
    parts.push(`--name ${shellQuote(cfg.name.trim())}`)
  }
  const net = normalizeNetMode(cfg.networkMode)
  if (net && net !== 'bridge') {
    parts.push(`--network ${shellQuote(net)}`)
  }
  const rp = normalizeRestart(cfg.restartPolicy)
  if (rp) {
    parts.push(`--restart ${shellQuote(rp)}`)
  }
  if (cfg.user.trim()) {
    parts.push(`--user ${shellQuote(cfg.user.trim())}`)
  }
  if (cfg.privileged) {
    parts.push('--privileged')
  }
  if (cfg.hostname.trim()) {
    parts.push(`--hostname ${shellQuote(cfg.hostname.trim())}`)
  }
  for (const cap of cfg.capAdd) {
    if (cap.trim()) parts.push(`--cap-add ${shellQuote(cap.trim())}`)
  }
  for (const eh of cfg.extraHosts) {
    if (eh.trim()) parts.push(`--add-host ${shellQuote(eh.trim())}`)
  }
  for (const ef of cfg.extraFlags) {
    if (ef.trim()) parts.push(ef.trim())
  }
  for (const p of cfg.ports) {
    const hp = p.hostPort.trim()
    const cp = p.containerPort.trim()
    if (!hp && !cp) continue
    const ipPart = p.hostIp?.trim() ? `${p.hostIp.trim()}:` : ''
    const proto = (p.protocol || 'tcp').toLowerCase()
    const protoPart = proto !== 'tcp' ? `/${proto}` : ''
    parts.push(`-p ${shellQuote(`${ipPart}${hp}:${cp}${protoPart}`)}`)
  }
  for (const e of cfg.envs) {
    const k = e.key.trim()
    const v = e.value
    if (!k && !v) continue
    parts.push(`-e ${shellQuote(`${k}=${v}`)}`)
  }
  for (const v of cfg.volumes) {
    const hp = v.hostPath.trim()
    const cp = v.containerPath.trim()
    if (!hp && !cp) continue
    const roPart = v.mode?.toLowerCase() === 'ro' ? ':ro' : ''
    const spec = hp ? `${hp}:${cp}${roPart}` : cp
    parts.push(`-v ${shellQuote(spec)}`)
  }
  for (const d of cfg.devices) {
    const hp = d.hostPath.trim()
    const cp = (d.containerPath || d.hostPath).trim()
    if (!hp && !cp) continue
    const permPart =
      d.permissions && d.permissions !== 'rwm' ? `:${d.permissions.trim()}` : ''
    parts.push(`--device ${shellQuote(`${hp || cp}:${cp || hp}${permPart}`)}`)
  }
  if (cfg.image.trim()) {
    parts.push(shellQuote(cfg.image.trim()))
  }
  if (cfg.cmdArgs.length > 0) {
    parts.push(cfg.cmdArgs.map(shellQuote).join(' '))
  }
  return parts.join(' \\\n  ')
}

function tokenizeShell(raw: string): string[] {
  const joined = raw.replace(/\\\r?\n/g, ' ')
  const tokens: string[] = []
  let cur = ''
  let quote: '"' | "'" | null = null
  let inToken = false

  for (let i = 0; i < joined.length; i++) {
    const ch = joined[i]
    if (quote === "'") {
      if (ch === "'") quote = null
      else cur += ch
      continue
    }
    if (quote === '"') {
      if (ch === '\\' && i + 1 < joined.length) {
        cur += joined[++i]
      } else if (ch === '"') {
        quote = null
      } else {
        cur += ch
      }
      continue
    }
    if (ch === "'" || ch === '"') {
      quote = ch
      inToken = true
      continue
    }
    if (ch === '\\' && i + 1 < joined.length) {
      cur += joined[++i]
      inToken = true
      continue
    }
    if (/\s/.test(ch)) {
      if (inToken) {
        tokens.push(cur)
        cur = ''
        inToken = false
      }
      continue
    }
    cur += ch
    inToken = true
  }
  if (inToken) tokens.push(cur)
  return tokens
}

function parsePortSpec(raw: string): ServicePortItem {
  let s = raw.trim().replace(/^["']|["']$/g, '')
  let protocol = 'tcp'
  const slashIdx = s.lastIndexOf('/')
  if (slashIdx >= 0) {
    protocol = s.slice(slashIdx + 1).toLowerCase() || 'tcp'
    s = s.slice(0, slashIdx)
  }
  const parts = s.split(':')
  if (parts.length >= 3) {
    return {
      hostIp: parts.slice(0, -2).join(':'),
      hostPort: parts[parts.length - 2],
      containerPort: parts[parts.length - 1],
      protocol,
    }
  }
  if (parts.length === 2) {
    return { hostPort: parts[0], containerPort: parts[1], protocol }
  }
  return { hostPort: s, containerPort: s, protocol }
}

function parseVolumeSpec(raw: string): ServiceVolumeItem {
  const s = raw.trim().replace(/^["']|["']$/g, '')
  const parts = s.split(':')
  if (parts.length >= 3) {
    const last = parts[parts.length - 1].toLowerCase()
    if (last === 'ro' || last === 'rw' || last === 'z' || last === 'Z') {
      return {
        hostPath: parts.slice(0, -2).join(':'),
        containerPath: parts[parts.length - 2],
        mode: last === 'ro' ? 'ro' : 'rw',
      }
    }
  }
  if (parts.length >= 2) {
    return {
      hostPath: parts[0],
      containerPath: parts.slice(1).join(':'),
      mode: 'rw',
    }
  }
  return { hostPath: '', containerPath: s, mode: 'rw' }
}

function parseDeviceSpec(raw: string): ServiceDeviceItem {
  const s = raw.trim().replace(/^["']|["']$/g, '')
  const parts = s.split(':')
  if (parts.length >= 3) {
    return {
      hostPath: parts[0],
      containerPath: parts[1],
      permissions: parts[2],
    }
  }
  if (parts.length === 2) {
    return { hostPath: parts[0], containerPath: parts[1] }
  }
  return { hostPath: s, containerPath: s }
}

const VALUE_FLAGS = new Set([
  '--entrypoint',
  '--workdir',
  '-w',
  '--memory',
  '-m',
  '--cpus',
  '--shm-size',
  '--dns',
  '--ip',
  '--ip6',
  '--mac-address',
  '--pid',
  '--ipc',
  '--uts',
  '--log-driver',
  '--log-opt',
  '--label',
  '-l',
  '--env-file',
  '--security-opt',
  '--sysctl',
  '--ulimit',
  '--cap-drop',
  '--group-add',
  '--runtime',
  '--gpus',
])

/** 解析 docker run 命令字符串为结构化配置 */
export function parseDockerRunCommand(
  rawText: string,
  fallbackName = '',
): EditableServiceConfig {
  const toks = tokenizeShell(rawText)
  const out: EditableServiceConfig = {
    name: fallbackName,
    image: '',
    networkMode: 'bridge',
    restartPolicy: '',
    user: '',
    privileged: false,
    capAdd: [],
    hostname: '',
    extraHosts: [],
    extraFlags: [],
    cmdArgs: [],
    envs: [],
    ports: [],
    volumes: [],
    devices: [],
  }

  let i = 0
  if (toks[0] === 'sudo') i++
  if (toks[i] === 'docker' && toks[i + 1] === 'run') i += 2
  else if (toks[i] === 'docker' && toks[i + 1] === 'container' && toks[i + 2] === 'run') i += 3

  const readOptVal = (flag: string): string | null => {
    const t = toks[i]
    if (t === flag && i + 1 < toks.length) {
      i += 2
      return toks[i - 1]
    }
    if (t.startsWith(`${flag}=`)) {
      i += 1
      return t.slice(flag.length + 1)
    }
    return null
  }

  while (i < toks.length) {
    const t = toks[i]
    if (t === '-d' || t === '--detach' || t === '-it' || t === '-ti' || t === '-i' || t === '-t') {
      i++
      continue
    }
    if (t === '--privileged') {
      out.privileged = true
      i++
      continue
    }
    if (t.startsWith('--privileged=')) {
      out.privileged = t.slice(13).toLowerCase() !== 'false'
      i++
      continue
    }

    let val: string | null = null
    if ((val = readOptVal('--name')) !== null) {
      out.name = val
      continue
    }
    if ((val = readOptVal('--network')) !== null || (val = readOptVal('--net')) !== null) {
      out.networkMode = normalizeNetMode(val)
      continue
    }
    if ((val = readOptVal('--restart')) !== null) {
      out.restartPolicy = normalizeRestart(val)
      continue
    }
    if ((val = readOptVal('--user')) !== null || (val = readOptVal('-u')) !== null) {
      out.user = val
      continue
    }
    if ((val = readOptVal('--hostname')) !== null || (val = readOptVal('-h')) !== null) {
      out.hostname = val
      continue
    }
    if ((val = readOptVal('--cap-add')) !== null) {
      out.capAdd.push(val)
      continue
    }
    if ((val = readOptVal('--add-host')) !== null) {
      out.extraHosts.push(val)
      continue
    }
    if ((val = readOptVal('-e')) !== null || (val = readOptVal('--env')) !== null) {
      const eq = val.indexOf('=')
      if (eq >= 0) out.envs.push({ key: val.slice(0, eq), value: val.slice(eq + 1) })
      else out.envs.push({ key: val, value: '' })
      continue
    }
    if ((val = readOptVal('-p')) !== null || (val = readOptVal('--publish')) !== null) {
      out.ports.push(parsePortSpec(val))
      continue
    }
    if ((val = readOptVal('-v')) !== null || (val = readOptVal('--volume')) !== null) {
      out.volumes.push(parseVolumeSpec(val))
      continue
    }
    if ((val = readOptVal('--device')) !== null) {
      out.devices.push(parseDeviceSpec(val))
      continue
    }

    if (t.startsWith('-')) {
      if (VALUE_FLAGS.has(t) && i + 1 < toks.length) {
        out.extraFlags.push(`${t} ${shellQuote(toks[i + 1])}`)
        i += 2
      } else {
        out.extraFlags.push(t)
        i++
      }
      continue
    }

    out.image = t
    out.cmdArgs = toks.slice(i + 1)
    break
  }

  return out
}

/* ==================== Compose YAML 解析与原位局部写回 ==================== */

function countIndent(line: string): number {
  let n = 0
  for (let i = 0; i < line.length; i++) {
    if (line[i] === ' ') n += 1
    else if (line[i] === '\t') n += 2
    else break
  }
  return n
}

function unquoteYamlScalar(raw: string): string {
  let s = raw.trim()
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1)
  }
  const hashIdx = s.indexOf(' #')
  if (hashIdx >= 0) {
    s = s.slice(0, hashIdx).trim()
  }
  return s
}

function formatYamlScalar(val: string): string {
  if (val === '') return '""'
  if (/[:#{}[\],&*?|<>=!%@`]|^\s|\s$/.test(val) || /^(true|false|null|yes|no|on|off|\d+(\.\d+)?)$/i.test(val)) {
    return `"${val.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }
  return val
}

interface ServiceBlockRange {
  name: string
  headerLine: number
  startLine: number
  endLine: number
  svcIndent: number
  propIndent: number
}

function locateComposeServiceBlocks(lines: string[]): ServiceBlockRange[] {
  let inServices = false
  let servicesIndent = -1
  let svcIndent = -1
  const blocks: ServiceBlockRange[] = []
  const keyRe = /^["']?([A-Za-z0-9._-]+)["']?\s*:(?:\s*(?:#.*)?)?$/

  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const ind = countIndent(lines[i])

    if (!inServices) {
      if (/^services\s*:(?:\s*#.*)?$/.test(trimmed)) {
        inServices = true
        servicesIndent = ind
        svcIndent = -1
      }
      continue
    }

    if (ind <= servicesIndent) {
      if (blocks.length > 0 && blocks[blocks.length - 1].endLine < 0) {
        blocks[blocks.length - 1].endLine = i
      }
      inServices = false
      if (/^services\s*:(?:\s*#.*)?$/.test(trimmed)) {
        inServices = true
        servicesIndent = ind
        svcIndent = -1
      }
      continue
    }

    const m = keyRe.exec(trimmed)
    if (m && (svcIndent < 0 || ind === svcIndent)) {
      if (svcIndent < 0) svcIndent = ind
      if (blocks.length > 0 && blocks[blocks.length - 1].endLine < 0) {
        blocks[blocks.length - 1].endLine = i
      }
      blocks.push({
        name: m[1],
        headerLine: i,
        startLine: i + 1,
        endLine: -1,
        svcIndent: ind,
        propIndent: ind + 2,
      })
    } else if (blocks.length > 0) {
      const cur = blocks[blocks.length - 1]
      if (cur.propIndent === cur.svcIndent + 2 && ind > cur.svcIndent) {
        // 记录真实首属性缩进
        if (cur.startLine === i || countIndent(lines[cur.startLine] ?? '') <= cur.svcIndent) {
          cur.propIndent = ind
        }
      }
    }
  }

  if (blocks.length > 0 && blocks[blocks.length - 1].endLine < 0) {
    blocks[blocks.length - 1].endLine = lines.length
  }

  for (const b of blocks) {
    for (let j = b.startLine; j < b.endLine; j++) {
      const t = lines[j].trim()
      if (!t || t.startsWith('#')) continue
      const ind = countIndent(lines[j])
      if (ind > b.svcIndent) {
        b.propIndent = ind
        break
      }
    }
  }

  return blocks
}

export function parseComposeServices(yamlContent: string): EditableServiceConfig[] {
  if (!yamlContent.trim()) return []
  const lines = yamlContent.split(/\r?\n/)
  const blocks = locateComposeServiceBlocks(lines)

  return blocks.map((b) => {
    const cfg: EditableServiceConfig = {
      name: b.name,
      image: '',
      networkMode: 'bridge',
      restartPolicy: '',
      user: '',
      privileged: false,
      capAdd: [],
      hostname: '',
      extraHosts: [],
      extraFlags: [],
      cmdArgs: [],
      envStyle: 'list',
      envs: [],
      ports: [],
      volumes: [],
      devices: [],
    }

    let curProp = ''
    let propBaseIndent = b.propIndent

    for (let i = b.startLine; i < b.endLine; i++) {
      const raw = lines[i]
      const trimmed = raw.trim()
      if (!trimmed || trimmed.startsWith('#')) continue
      const ind = countIndent(raw)

      if (ind === b.propIndent) {
        curProp = ''
        const colonIdx = trimmed.indexOf(':')
        if (colonIdx < 0) continue
        const key = trimmed.slice(0, colonIdx).trim()
        const rest = trimmed.slice(colonIdx + 1).trim()

        if (key === 'image' && rest) {
          cfg.image = unquoteYamlScalar(rest)
        } else if (key === 'network_mode' && rest) {
          cfg.networkMode = normalizeNetMode(unquoteYamlScalar(rest))
          cfg.networkSource = 'network_mode'
        } else if (key === 'restart' && rest) {
          cfg.restartPolicy = normalizeRestart(unquoteYamlScalar(rest))
        } else if (key === 'user' && rest) {
          cfg.user = unquoteYamlScalar(rest)
        } else if (key === 'privileged' && rest) {
          cfg.privileged = unquoteYamlScalar(rest).toLowerCase() === 'true'
        } else if (key === 'hostname' && rest) {
          cfg.hostname = unquoteYamlScalar(rest)
        } else if (
          key === 'environment' ||
          key === 'ports' ||
          key === 'volumes' ||
          key === 'devices' ||
          key === 'networks' ||
          key === 'cap_add'
        ) {
          if (!rest || rest.startsWith('#')) {
            curProp = key
            propBaseIndent = ind
          }
        }
        continue
      }

      if (ind > propBaseIndent && curProp) {
        if (curProp === 'environment') {
          if (trimmed.startsWith('-')) {
            cfg.envStyle = 'list'
            const item = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            const eq = item.indexOf('=')
            if (eq >= 0) {
              cfg.envs.push({ key: item.slice(0, eq).trim(), value: item.slice(eq + 1) })
            } else {
              const col = item.indexOf(':')
              if (col >= 0) {
                cfg.envs.push({
                  key: item.slice(0, col).trim(),
                  value: unquoteYamlScalar(item.slice(col + 1)),
                })
              } else if (item) {
                cfg.envs.push({ key: item, value: '' })
              }
            }
          } else {
            const col = trimmed.indexOf(':')
            if (col > 0) {
              if (cfg.envs.length === 0) cfg.envStyle = 'map'
              const k = unquoteYamlScalar(trimmed.slice(0, col))
              const v = unquoteYamlScalar(trimmed.slice(col + 1))
              cfg.envs.push({ key: k, value: v })
            }
          }
        } else if (curProp === 'ports') {
          if (trimmed.startsWith('-')) {
            const item = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            if (item && !item.includes(': ') && !item.startsWith('target:')) {
              cfg.ports.push(parsePortSpec(item))
            }
          }
        } else if (curProp === 'volumes') {
          if (trimmed.startsWith('-')) {
            const item = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            if (item && !item.includes(': ') && !item.startsWith('type:')) {
              cfg.volumes.push(parseVolumeSpec(item))
            }
          }
        } else if (curProp === 'devices') {
          if (trimmed.startsWith('-')) {
            const item = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            if (item) cfg.devices.push(parseDeviceSpec(item))
          }
        } else if (curProp === 'networks' && !cfg.networkSource) {
          if (trimmed.startsWith('-')) {
            const netName = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            if (netName) {
              cfg.networkMode = netName
              cfg.networkSource = 'networks'
            }
          } else if (trimmed.endsWith(':')) {
            const netName = unquoteYamlScalar(trimmed.slice(0, -1))
            if (netName) {
              cfg.networkMode = netName
              cfg.networkSource = 'networks'
            }
          }
        } else if (curProp === 'cap_add') {
          if (trimmed.startsWith('-')) {
            const cap = unquoteYamlScalar(trimmed.replace(/^-\s*/, ''))
            if (cap) cfg.capAdd.push(cap)
          }
        }
      }
    }

    return cfg
  })
}

interface PropRange {
  start: number
  end: number
}

function findServicePropRanges(
  lines: string[],
  block: ServiceBlockRange,
): Record<string, PropRange> {
  const map: Record<string, PropRange> = {}
  let curKey = ''
  let curStart = -1

  for (let i = block.startLine; i < block.endLine; i++) {
    const trimmed = lines[i].trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const ind = countIndent(lines[i])
    if (ind === block.propIndent) {
      if (curKey && curStart >= 0) {
        map[curKey] = { start: curStart, end: i }
      }
      const col = trimmed.indexOf(':')
      if (col > 0) {
        curKey = trimmed.slice(0, col).trim()
        curStart = i
      } else {
        curKey = ''
        curStart = -1
      }
    }
  }
  if (curKey && curStart >= 0) {
    let end = block.endLine
    while (end > curStart && lines[end - 1].trim() === '') {
      end--
    }
    map[curKey] = { start: curStart, end }
  }
  return map
}

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

/** 将单个服务在表单中的改动原位局部写回 Compose YAML，保留未改动区块与注释 */
export function patchComposeServiceInYaml(
  yamlContent: string,
  svcName: string,
  next: EditableServiceConfig,
): string {
  const eol = yamlContent.includes('\r\n') ? '\r\n' : '\n'
  const lines = yamlContent.split(/\r?\n/)
  const blocks = locateComposeServiceBlocks(lines)
  const block = blocks.find((b) => b.name === svcName)
  if (!block) return yamlContent

  const currentParsed = parseComposeServices(yamlContent).find((s) => s.name === svcName)
  if (!currentParsed) return yamlContent

  const pad = ' '.repeat(block.propIndent)
  const itemPad = ' '.repeat(block.propIndent + 2)

  const applyKeyPatch = (workLines: string[], key: string, newLines: string[] | null) => {
    const curBlocks = locateComposeServiceBlocks(workLines)
    const b = curBlocks.find((x) => x.name === svcName)
    if (!b) return
    const ranges = findServicePropRanges(workLines, b)
    const r = ranges[key]
    if (r) {
      if (newLines === null || newLines.length === 0) {
        workLines.splice(r.start, r.end - r.start)
      } else {
        workLines.splice(r.start, r.end - r.start, ...newLines)
      }
    } else if (newLines && newLines.length > 0) {
      let insertAt = b.endLine
      while (insertAt > b.startLine && workLines[insertAt - 1].trim() === '') {
        insertAt--
      }
      workLines.splice(insertAt, 0, ...newLines)
    }
  }

  const work = [...lines]

  if (next.image.trim() && next.image.trim() !== currentParsed.image.trim()) {
    applyKeyPatch(work, 'image', [`${pad}image: ${next.image.trim()}`])
  }

  if (normalizeNetMode(next.networkMode) !== normalizeNetMode(currentParsed.networkMode)) {
    const nm = normalizeNetMode(next.networkMode)
    if (nm === 'bridge') {
      applyKeyPatch(work, 'network_mode', null)
      if (currentParsed.networkSource === 'networks') {
        applyKeyPatch(work, 'networks', null)
      }
    } else if (nm === 'host' || nm === 'none' || nm.startsWith('container:')) {
      if (currentParsed.networkSource === 'networks') {
        applyKeyPatch(work, 'networks', null)
      }
      applyKeyPatch(work, 'network_mode', [`${pad}network_mode: ${nm}`])
    } else if (currentParsed.networkSource === 'networks') {
      applyKeyPatch(work, 'networks', [`${pad}networks:`, `${itemPad}- ${nm}`])
    } else {
      applyKeyPatch(work, 'network_mode', [`${pad}network_mode: ${nm}`])
    }
  }

  if (normalizeRestart(next.restartPolicy) !== normalizeRestart(currentParsed.restartPolicy)) {
    const rp = normalizeRestart(next.restartPolicy)
    applyKeyPatch(work, 'restart', rp ? [`${pad}restart: ${rp}`] : null)
  }

  if (next.user.trim() !== currentParsed.user.trim()) {
    const u = next.user.trim()
    applyKeyPatch(work, 'user', u ? [`${pad}user: "${u}"`] : null)
  }

  if (Boolean(next.privileged) !== Boolean(currentParsed.privileged)) {
    applyKeyPatch(work, 'privileged', next.privileged ? [`${pad}privileged: true`] : null)
  }

  if (!sameJson(next.ports, currentParsed.ports)) {
    const validPorts = next.ports.filter((p) => p.hostPort.trim() || p.containerPort.trim())
    if (validPorts.length === 0) {
      applyKeyPatch(work, 'ports', null)
    } else {
      const pLines = [`${pad}ports:`]
      for (const p of validPorts) {
        const ipPart = p.hostIp?.trim() ? `${p.hostIp.trim()}:` : ''
        const proto = (p.protocol || 'tcp').toLowerCase()
        const protoPart = proto !== 'tcp' ? `/${proto}` : ''
        pLines.push(`${itemPad}- "${ipPart}${p.hostPort.trim()}:${p.containerPort.trim()}${protoPart}"`)
      }
      applyKeyPatch(work, 'ports', pLines)
    }
  }

  if (!sameJson(next.envs, currentParsed.envs)) {
    const validEnvs = next.envs.filter((e) => e.key.trim() || e.value.trim())
    if (validEnvs.length === 0) {
      applyKeyPatch(work, 'environment', null)
    } else {
      const eLines = [`${pad}environment:`]
      const useMap = (currentParsed.envStyle ?? next.envStyle) === 'map'
      for (const e of validEnvs) {
        if (useMap) {
          eLines.push(`${itemPad}${e.key.trim()}: ${formatYamlScalar(e.value)}`)
        } else {
          const itemStr = `${e.key.trim()}=${e.value}`
          eLines.push(`${itemPad}- ${/[:#{}[\]]|^\s|\s$/.test(itemStr) ? `"${itemStr.replace(/"/g, '\\"')}"` : itemStr}`)
        }
      }
      applyKeyPatch(work, 'environment', eLines)
    }
  }

  if (!sameJson(next.volumes, currentParsed.volumes)) {
    const validVols = next.volumes.filter((v) => v.hostPath.trim() || v.containerPath.trim())
    if (validVols.length === 0) {
      applyKeyPatch(work, 'volumes', null)
    } else {
      const vLines = [`${pad}volumes:`]
      for (const v of validVols) {
        const hp = v.hostPath.trim()
        const cp = v.containerPath.trim()
        const roPart = v.mode?.toLowerCase() === 'ro' ? ':ro' : ''
        const spec = hp ? `${hp}:${cp}${roPart}` : cp
        vLines.push(`${itemPad}- ${spec}`)
      }
      applyKeyPatch(work, 'volumes', vLines)
    }
  }

  if (!sameJson(next.devices, currentParsed.devices)) {
    const validDevs = next.devices.filter((d) => d.hostPath.trim() || d.containerPath.trim())
    if (validDevs.length === 0) {
      applyKeyPatch(work, 'devices', null)
    } else {
      const dLines = [`${pad}devices:`]
      for (const d of validDevs) {
        const hp = d.hostPath.trim()
        const cp = (d.containerPath || d.hostPath).trim()
        const permPart =
          d.permissions && d.permissions !== 'rwm' ? `:${d.permissions.trim()}` : ''
        dLines.push(`${itemPad}- "${hp || cp}:${cp || hp}${permPart}"`)
      }
      applyKeyPatch(work, 'devices', dLines)
    }
  }

  return work.join(eol)
}

/* ==================== 三分类对比（改值 / 新增 / 缺少）与仅还原改值项 ==================== */

function portKey(p: ServicePortItem): string {
  const cp = p.containerPort.trim()
  if (!cp) return ''
  return `${cp}/${(p.protocol || 'tcp').toLowerCase()}`
}

export function diffServiceConfig(
  origSvc: EditableServiceConfig | undefined,
  curSvc: EditableServiceConfig,
  labels: ConfigDiffI18n,
): ServiceConfigDiff {
  const out: ServiceConfigDiff = {
    runtime: [],
    envs: { changed: [], added: [], removed: [] },
    ports: { changed: [], added: [], removed: [] },
    volumes: { changed: [], added: [], removed: [] },
    devices: { changed: [], added: [], removed: [] },
  }
  if (!origSvc) return out

  const svcName = curSvc.name

  const origNet = normalizeNetMode(origSvc.networkMode)
  const curNet = normalizeNetMode(curSvc.networkMode)
  if (origNet !== curNet) {
    out.runtime.push({ serviceName: svcName, label: labels.diffNetLabel, from: curNet, to: origNet })
  }

  const origRp = normalizeRestart(origSvc.restartPolicy) || 'no'
  const curRp = normalizeRestart(curSvc.restartPolicy) || 'no'
  if (origRp !== curRp) {
    out.runtime.push({
      serviceName: svcName,
      label: labels.diffRestartLabel,
      from: curRp,
      to: origRp,
    })
  }

  const origUser = origSvc.user.trim()
  const curUser = curSvc.user.trim()
  if (origUser !== curUser) {
    out.runtime.push({
      serviceName: svcName,
      label: labels.diffUserLabel,
      from: curUser || labels.diffUserDefault,
      to: origUser || labels.diffUserDefault,
    })
  }

  if (Boolean(origSvc.privileged) !== Boolean(curSvc.privileged)) {
    out.runtime.push({
      serviceName: svcName,
      label: labels.diffPrivLabel,
      from: curSvc.privileged ? labels.diffPrivOn : labels.diffPrivOff,
      to: origSvc.privileged ? labels.diffPrivOn : labels.diffPrivOff,
    })
  }

  // 环境变量
  for (const ce of curSvc.envs) {
    const k = ce.key.trim()
    if (!k) continue
    const oe = origSvc.envs.find((x) => x.key.trim() === k)
    if (!oe) {
      out.envs.added.push(`${labels.diffEnvPrefix}${k}=${ce.value}`)
    } else if (oe.value !== ce.value) {
      out.envs.changed.push({
        serviceName: svcName,
        label: `${labels.diffEnvPrefix}${k}`,
        from: ce.value || labels.emptyVal,
        to: oe.value || labels.emptyVal,
      })
    }
  }
  for (const oe of origSvc.envs) {
    const k = oe.key.trim()
    if (k && !curSvc.envs.some((x) => x.key.trim() === k)) {
      out.envs.removed.push(`${labels.diffEnvPrefix}${k}=${oe.value}`)
    }
  }

  // 端口映射
  for (const cp of curSvc.ports) {
    const pk = portKey(cp)
    if (!pk) continue
    const op = origSvc.ports.find((x) => portKey(x) === pk)
    if (!op) {
      out.ports.added.push(`${labels.diffPortPrefix}${cp.hostPort}→${cp.containerPort}`)
    } else if (op.hostPort.trim() !== cp.hostPort.trim()) {
      out.ports.changed.push({
        serviceName: svcName,
        label: `${labels.diffPortPrefix}${cp.containerPort}`,
        from: cp.hostPort.trim() || labels.emptyVal,
        to: op.hostPort.trim(),
      })
    }
  }
  for (const op of origSvc.ports) {
    const pk = portKey(op)
    if (pk && !curSvc.ports.some((x) => portKey(x) === pk)) {
      out.ports.removed.push(`${labels.diffPortPrefix}${op.hostPort}→${op.containerPort}`)
    }
  }

  // 目录挂载
  for (const cv of curSvc.volumes) {
    const target = cv.containerPath.trim()
    if (!target) continue
    const ov = origSvc.volumes.find((x) => x.containerPath.trim() === target)
    if (!ov) {
      out.volumes.added.push(`${labels.diffVolPrefix}${cv.hostPath}→${target}`)
    } else if (ov.hostPath.trim() !== cv.hostPath.trim()) {
      out.volumes.changed.push({
        serviceName: svcName,
        label: `${labels.diffVolPrefix}${target}`,
        from: cv.hostPath.trim() || labels.emptyVal,
        to: ov.hostPath.trim(),
      })
    }
  }
  for (const ov of origSvc.volumes) {
    const target = ov.containerPath.trim()
    if (target && !curSvc.volumes.some((x) => x.containerPath.trim() === target)) {
      out.volumes.removed.push(`${labels.diffVolPrefix}${ov.hostPath}→${target}`)
    }
  }

  // 硬件直通
  for (const cd of curSvc.devices) {
    const target = (cd.containerPath || cd.hostPath).trim()
    if (!target) continue
    const od = origSvc.devices.find((x) => (x.containerPath || x.hostPath).trim() === target)
    if (!od) {
      out.devices.added.push(`${labels.diffDevPrefix}${cd.hostPath}→${target}`)
    } else if (od.hostPath.trim() !== cd.hostPath.trim()) {
      out.devices.changed.push({
        serviceName: svcName,
        label: `${labels.diffDevPrefix}${target}`,
        from: cd.hostPath.trim() || labels.emptyVal,
        to: od.hostPath.trim(),
      })
    }
  }
  for (const od of origSvc.devices) {
    const target = (od.containerPath || od.hostPath).trim()
    if (target && !curSvc.devices.some((x) => (x.containerPath || x.hostPath).trim() === target)) {
      out.devices.removed.push(`${labels.diffDevPrefix}${od.hostPath}→${target}`)
    }
  }

  return out
}

export function diffConfigLists(
  origList: EditableServiceConfig[],
  curList: EditableServiceConfig[],
  labels: ConfigDiffI18n,
): ConfigDiffSummary {
  const byService: Record<string, ServiceConfigDiff> = {}
  const changed: DiffChangedItem[] = []
  const added: string[] = []
  const removed: string[] = []
  const multi = curList.length > 1 || origList.length > 1

  for (let i = 0; i < curList.length; i++) {
    const cs = curList[i]
    const os =
      origList.find((o) => o.name === cs.name) ?? (origList.length === 1 && curList.length === 1 ? origList[0] : undefined)
    const d = diffServiceConfig(os, cs, labels)
    byService[cs.name] = d
    const prefix = multi ? `[${cs.name}] ` : ''
    for (const it of [
      ...d.runtime,
      ...d.envs.changed,
      ...d.ports.changed,
      ...d.volumes.changed,
      ...d.devices.changed,
    ]) {
      changed.push({ ...it, label: `${prefix}${it.label}` })
    }
    for (const s of [...d.envs.added, ...d.ports.added, ...d.volumes.added, ...d.devices.added]) {
      added.push(`${prefix}${s}`)
    }
    for (const s of [
      ...d.envs.removed,
      ...d.ports.removed,
      ...d.volumes.removed,
      ...d.devices.removed,
    ]) {
      removed.push(`${prefix}${s}`)
    }
  }

  return { byService, changed, added, removed }
}

/** 严格仅恢复【同名但值被改动】的项，保留镜像版本号，不删新增项、也不找回缺失项 */
export function restoreServiceChangedValues(
  origSvc: EditableServiceConfig,
  curSvc: EditableServiceConfig,
  section?: 'runtime' | 'envs' | 'ports' | 'volumes' | 'devices',
): EditableServiceConfig {
  const next = cloneEditableConfig(curSvc)

  if (!section || section === 'runtime') {
    next.networkMode = origSvc.networkMode
    next.networkSource = origSvc.networkSource
    next.restartPolicy = origSvc.restartPolicy
    next.user = origSvc.user
    next.privileged = origSvc.privileged
  }

  if (!section || section === 'envs') {
    next.envs = next.envs.map((ce) => {
      const k = ce.key.trim()
      if (!k) return ce
      const oe = origSvc.envs.find((x) => x.key.trim() === k)
      return oe && oe.value !== ce.value ? { ...ce, value: oe.value } : ce
    })
  }

  if (!section || section === 'ports') {
    next.ports = next.ports.map((cp) => {
      const pk = portKey(cp)
      if (!pk) return cp
      const op = origSvc.ports.find((x) => portKey(x) === pk)
      return op && op.hostPort.trim() !== cp.hostPort.trim()
        ? { ...cp, hostPort: op.hostPort, ...(op.hostIp ? { hostIp: op.hostIp } : {}) }
        : cp
    })
  }

  if (!section || section === 'volumes') {
    next.volumes = next.volumes.map((cv) => {
      const target = cv.containerPath.trim()
      if (!target) return cv
      const ov = origSvc.volumes.find((x) => x.containerPath.trim() === target)
      return ov && ov.hostPath.trim() !== cv.hostPath.trim()
        ? { ...cv, hostPath: ov.hostPath, mode: ov.mode ?? cv.mode }
        : cv
    })
  }

  if (!section || section === 'devices') {
    next.devices = next.devices.map((cd) => {
      const target = (cd.containerPath || cd.hostPath).trim()
      if (!target) return cd
      const od = origSvc.devices.find((x) => (x.containerPath || x.hostPath).trim() === target)
      return od && od.hostPath.trim() !== cd.hostPath.trim()
        ? { ...cd, hostPath: od.hostPath }
        : cd
    })
  }

  return next
}
