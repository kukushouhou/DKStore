const SAMPLE_PLACEHOLDERS = new Set([
  'password',
  'secret',
  'secretkey',
  'changeme',
  'your_password',
  'your_secret',
  'example',
  '123456',
  'admin',
  'postgres',
  'root',
])

export function normalizeImageRepo(imageRef: string): string {
  const withoutDigest = imageRef.trim().split('@')[0]
  const lastSlash = withoutDigest.lastIndexOf('/')
  const colonAfterSlash = withoutDigest.indexOf(':', lastSlash + 1)
  const repoPath = colonAfterSlash >= 0 ? withoutDigest.slice(0, colonAfterSlash) : withoutDigest
  return repoPath
    .replace(/^docker\.io\/library\//, '')
    .replace(/^docker\.io\//, '')
    .replace(/^index\.docker\.io\/library\//, '')
    .replace(/^index\.docker\.io\//, '')
    .replace(/^library\//, '')
    .toLowerCase()
}

/** 将 YAML 配置中对应容器的 image: 标签同步替换为当前选定的版本号，保留原有注释与缩进 */
export function replaceComposeImageTag(
  yamlContent: string,
  serviceImageRef: string,
  nextTag: string,
): string {
  if (!yamlContent || !nextTag.trim()) return yamlContent
  const targetTag = nextTag.trim()
  const targetRepo = normalizeImageRepo(serviceImageRef)
  const lines = yamlContent.split('\n')
  const imgRegex = /^(\s*image\s*:\s*)(["']?)([^\s#"']+)(["']?)(\s*(?:#.*)?\r?)$/i

  const matchIndices: number[] = []
  const allImageIndices: number[] = []

  for (let i = 0; i < lines.length; i++) {
    const m = imgRegex.exec(lines[i])
    if (!m) continue
    allImageIndices.push(i)
    if (targetRepo && normalizeImageRepo(m[3]) === targetRepo) {
      matchIndices.push(i)
    }
  }

  const indicesToUpdate =
    matchIndices.length > 0
      ? matchIndices
      : allImageIndices.length === 1
        ? allImageIndices
        : []

  if (indicesToUpdate.length === 0) return yamlContent

  const nextLines = [...lines]
  for (const idx of indicesToUpdate) {
    const m = imgRegex.exec(nextLines[idx])
    if (!m) continue
    const [, prefix, openQuote, rawRef, closeQuote, trailing] = m
    const withoutDigest = rawRef.split('@')[0]
    const lastSlash = withoutDigest.lastIndexOf('/')
    const colonAfterSlash = withoutDigest.indexOf(':', lastSlash + 1)
    const repoPart = colonAfterSlash >= 0 ? withoutDigest.slice(0, colonAfterSlash) : withoutDigest
    nextLines[idx] = `${prefix}${openQuote}${repoPart}:${targetTag}${closeQuote}${trailing}`
  }

  return nextLines.join('\n')
}

function countYamlIndent(line: string): number {
  let n = 0
  for (let i = 0; i < line.length; i++) {
    const ch = line[i]
    if (ch === ' ') n += 1
    else if (ch === '\t') n += 2
    else break
  }
  return n
}

/** 按声明顺序提取 Compose YAML 中 services 块下的全部容器服务名称 */
export function parseComposeServiceNames(yamlContent: string): string[] {
  if (!yamlContent || !yamlContent.trim()) return []
  const lines = yamlContent.split(/\r?\n/)
  let inServices = false
  let servicesIndent = -1
  let childKeyIndent = -1
  const names: string[] = []
  const seen = new Set<string>()
  const keyRegex = /^["']?([A-Za-z0-9._-]+)["']?\s*:(?:\s*(?:#.*|\{.*\}))?$/

  for (const rawLine of lines) {
    const trimmed = rawLine.trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const indent = countYamlIndent(rawLine)

    if (!inServices) {
      if (/^services\s*:(?:\s*#.*)?$/.test(trimmed)) {
        inServices = true
        servicesIndent = indent
        childKeyIndent = -1
      }
      continue
    }

    if (indent <= servicesIndent) {
      inServices = false
      childKeyIndent = -1
      if (/^services\s*:(?:\s*#.*)?$/.test(trimmed)) {
        inServices = true
        servicesIndent = indent
      }
      continue
    }

    const m = keyRegex.exec(trimmed)
    if (!m) continue
    if (childKeyIndent < 0) {
      childKeyIndent = indent
    }
    if (indent === childKeyIndent) {
      const svcName = m[1]
      if (svcName && !seen.has(svcName)) {
        seen.add(svcName)
        names.push(svcName)
      }
    }
  }

  return names
}

export interface ComposeServiceDiffOptions {
  effectiveOrigServices?: string[]
  extraFileServices?: string[]
}

export interface ComposeOverwriteRisks {
  missingPaths: string[]
  overwrittenSecrets: string[]
  missingServices: string[]
  addedServices: string[]
}

/** 提取原配置中的宿主机绝对挂载路径、敏感环境变量与容器集合，检测修改后的路径覆盖、密钥丢失与容器减少 */
export function detectComposeOverwriteRisks(
  origYaml: string,
  nextYaml: string,
  opts?: ComposeServiceDiffOptions,
): ComposeOverwriteRisks {
  if ((!origYaml.trim() && !(opts?.effectiveOrigServices?.length)) || !nextYaml.trim()) {
    return {
      missingPaths: [],
      overwrittenSecrets: [],
      missingServices: [],
      addedServices: [],
    }
  }

  const origServiceList =
    opts?.effectiveOrigServices && opts.effectiveOrigServices.length > 0
      ? Array.from(new Set(opts.effectiveOrigServices.map((s) => s.trim()).filter(Boolean)))
      : parseComposeServiceNames(origYaml)
  const nextPrimaryServices = parseComposeServiceNames(nextYaml)
  const extraServices = (opts?.extraFileServices ?? []).map((s) => s.trim()).filter(Boolean)

  let missingServices: string[] = []
  let addedServices: string[] = []
  if (origServiceList.length > 0 && (nextPrimaryServices.length > 0 || extraServices.length > 0)) {
    const effectiveNextList = Array.from(new Set([...nextPrimaryServices, ...extraServices]))
    const nextSet = new Set(effectiveNextList)
    const origSet = new Set(origServiceList)
    missingServices = origServiceList.filter((name) => !nextSet.has(name))
    addedServices = effectiveNextList.filter((name) => !origSet.has(name))
  }

  if (!origYaml.trim()) {
    return {
      missingPaths: [],
      overwrittenSecrets: [],
      missingServices,
      addedServices,
    }
  }

  const hostPaths = new Set<string>()
  const lines = origYaml.split(/\r?\n/)
  const volRegex = /^\s*-\s*["']?(\/[A-Za-z0-9._\-/]+):/
  const sourceRegex = /^\s*source:\s*["']?(\/[A-Za-z0-9._\-/]+)["']?\s*$/

  for (const line of lines) {
    const m = volRegex.exec(line) || sourceRegex.exec(line)
    if (m && m[1]) {
      const p = m[1]
      if (p !== '/etc/localtime' && p !== '/etc/timezone') {
        hostPaths.add(p)
      }
    }
  }

  const missingPaths = [...hostPaths].filter((p) => !nextYaml.includes(p))

  const secretEntries = new Map<string, string>()
  const envListRegex =
    /^\s*-\s*([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|KEY|TOKEN)[A-Z0-9_]*)\s*=\s*(.+?)\s*$/i
  const envMapRegex =
    /^\s*([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|KEY|TOKEN)[A-Z0-9_]*)\s*:\s*(.+?)\s*$/i

  for (const line of lines) {
    const m = envListRegex.exec(line) || envMapRegex.exec(line)
    if (m && m[1] && m[2]) {
      const key = m[1]
      const val = m[2].replace(/^["']|["']$/g, '').trim()
      if (val && !val.startsWith('${') && !SAMPLE_PLACEHOLDERS.has(val.toLowerCase())) {
        secretEntries.set(key, val)
      }
    }
  }

  const overwrittenSecrets: string[] = []
  for (const [key, origVal] of secretEntries) {
    if (!nextYaml.includes(origVal)) {
      overwrittenSecrets.push(key)
    }
  }

  return { missingPaths, overwrittenSecrets, missingServices, addedServices }
}
