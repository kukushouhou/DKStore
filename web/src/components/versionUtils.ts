function isShortHexHash(s: string): boolean {
  return /^[0-9a-f]{6}$/i.test(s)
}

/** 从 "2.6 (2.6.8)" / "latest (v3.16.0)" 中提取真实具体版本号；若仅含短哈希如 "latest (3d9048)" 则返回空串 */
export function unwrapConcreteVersion(tag?: string): string {
  if (!tag) return ''
  const s = tag.trim()
  const idx = s.indexOf(' (')
  if (idx > 0 && s.endsWith(')')) {
    const base = s.slice(0, idx).trim()
    const inner = s.slice(idx + 2, -1).trim()
    if (inner && !isShortHexHash(inner)) {
      return inner
    }
    if (/^v?\d+(\.\d+)*$/i.test(base)) {
      return base
    }
    return ''
  }
  return s
}

function parseCoreSegs(raw: string): string[] {
  const clean = raw.trim().replace(/^v/i, '').split(/[-+_]/)[0]
  if (!/^\d+(\.\d+)+$/.test(clean)) return []
  return clean.split('.')
}

/** 提取多段纯数字版本号（至少 2 段，支持从 "8.4 (8.4.11)" / "latest (v3.9.1)" 括号内提取） */
export function parseNumericSegs(tag?: string): string[] {
  if (!tag) return []
  const s = tag.trim()
  const idx = s.indexOf(' (')
  if (idx > 0 && s.endsWith(')')) {
    const inner = s.slice(idx + 2, -1).trim()
    if (inner && !isShortHexHash(inner)) {
      const innerSegs = parseCoreSegs(inner)
      if (innerSegs.length > 0) return innerSegs
    }
    return parseCoreSegs(s.slice(0, idx))
  }
  return parseCoreSegs(s)
}

interface ParsedPrereleaseMeta {
  prerelease: boolean
  stage: number // 1=dev/nightly/canary/snapshot/test, 2=alpha/preview, 3=beta, 4=rc
  nums: number[]
}

const RE_PRERELEASE_TOKEN =
  /^(alpha|beta|rc|dev|preview|snapshot|nightly|canary|test)(?:[.]?(\d+(?:\.\d+)*))?$/i
const RE_CHANNEL_PRERELEASE =
  /^(alpha|beta|rc|dev|devel|develop|next|preview|snapshot|nightly|canary|edge|testing)\d*(?:[-_][a-z][a-z0-9]*)?$/i

function prereleaseStageOf(kw: string): number {
  const low = kw.toLowerCase()
  if (low === 'rc') return 4
  if (low === 'beta') return 3
  if (low === 'alpha' || low === 'preview') return 2
  return 1
}

function parsePrereleaseFromRaw(raw?: string): ParsedPrereleaseMeta {
  if (!raw) return { prerelease: false, stage: 0, nums: [] }
  const base = raw.trim().split(' (')[0].trim()
  const dashIdx = base.search(/[-+_]/)
  if (dashIdx < 0) return { prerelease: false, stage: 0, nums: [] }
  const suffix = base.slice(dashIdx + 1)
  const parts = suffix.split(/[-_]/).filter(Boolean)
  for (let i = 0; i < parts.length; i++) {
    const m = parts[i].match(RE_PRERELEASE_TOKEN)
    if (m) {
      let numStr = m[2] || ''
      if (!numStr && i + 1 < parts.length && /^\d+(?:\.\d+)*$/.test(parts[i + 1])) {
        numStr = parts[i + 1]
      }
      const nums = numStr ? numStr.split('.').map((n) => Number(n)) : []
      return { prerelease: true, stage: prereleaseStageOf(m[1]), nums }
    }
  }
  return { prerelease: false, stage: 0, nums: [] }
}

/** 判断标签是否属于预发布版本（含 3.0.0-beta.1 及 :beta / :rc / :next / :nightly 通道标签） */
export function isPrereleaseTagStr(tag?: string): boolean {
  if (!tag) return false
  const clean = tag.trim().split(' (')[0].trim()
  if (!clean) return false
  const lower = clean.toLowerCase()
  if (
    lower === 'latest' ||
    lower === 'stable' ||
    lower === 'release' ||
    lower === 'prod' ||
    lower === 'production' ||
    lower === 'main' ||
    lower === 'master'
  ) {
    return false
  }
  if (parsePrereleaseFromRaw(clean).prerelease) return true
  if (!/^\d/.test(lower) && !/^v\d/.test(lower) && RE_CHANNEL_PRERELEASE.test(lower)) {
    return true
  }
  return false
}

/** 逐段比较两个版本号大小（含预发布阶段顺序 3.0.0 > 3.0.0-rc.1 > 3.0.0-beta.2 > 3.0.0-beta.1）：a > b 返回正数，a < b 返回负数，无法比较或相等返回 0 */
export function compareNumericSegs(tagA?: string, tagB?: string): number {
  const segA = parseNumericSegs(tagA)
  const segB = parseNumericSegs(tagB)
  if (segA.length === 0 || segB.length === 0) return 0
  const maxLen = Math.max(segA.length, segB.length)
  for (let i = 0; i < maxLen; i++) {
    const na = Number(segA[i] ?? 0)
    const nb = Number(segB[i] ?? 0)
    if (na !== nb) return na - nb
  }
  const preA = parsePrereleaseFromRaw(tagA)
  const preB = parsePrereleaseFromRaw(tagB)
  if (preA.prerelease !== preB.prerelease) {
    return preA.prerelease ? -1 : 1
  }
  if (preA.prerelease && preB.prerelease) {
    if (preA.stage !== preB.stage) return preA.stage - preB.stage
    const pLen = Math.max(preA.nums.length, preB.nums.length)
    for (let i = 0; i < pLen; i++) {
      const pa = preA.nums[i] ?? 0
      const pb = preB.nums[i] ?? 0
      if (pa !== pb) return pa - pb
    }
  }
  return 0
}
