export interface ModelMappingEntry {
  from: string
  to: string
}

export interface ModelMappingAutoRule {
  enabled: boolean
  from: string
  to: string
  source?: string
  updated_at?: string
}

export interface ModelMappingAutomationSettings {
  rules: ModelMappingAutoRule[]
  batch_test_concurrency: number
}

export interface ModelMappingSuggestion extends ModelMappingEntry {
  reason: 'rule' | 'lowercase' | 'provider_prefix'
}

const DEFAULT_BATCH_TEST_CONCURRENCY = 3
const MAX_BATCH_TEST_CONCURRENCY = 10

const normalizeName = (value: string) => value.trim()

function deriveAutoMappingSuggestion(actualModel: string): ModelMappingSuggestion | null {
  const actual = normalizeName(actualModel)
  if (!actual) return null

  const slashIndex = actual.lastIndexOf('/')
  if (slashIndex > 0 && slashIndex < actual.length - 1) {
    const requestModel = normalizeName(actual.slice(slashIndex + 1)).toLowerCase()
    if (requestModel && !requestModel.includes('/') && requestModel !== actual) {
      return {
        from: requestModel,
        to: actual,
        reason: 'provider_prefix'
      }
    }
  }

  const lowercaseRequestModel = actual.toLowerCase()
  if (actual !== lowercaseRequestModel && /[A-Z]/.test(actual)) {
    return {
      from: lowercaseRequestModel,
      to: actual,
      reason: 'lowercase'
    }
  }

  return null
}

function normalizeAutoDiscoveredRuleDirection(rule: ModelMappingAutoRule): ModelMappingAutoRule {
  if (normalizeName(rule.source || '').toLowerCase() !== 'auto_discovered') return rule

  const corrected = deriveAutoMappingSuggestion(rule.from || '')
  if (!corrected || corrected.from !== normalizeName(rule.to)) return rule

  return {
    ...rule,
    from: corrected.from,
    to: corrected.to
  }
}

export function normalizeModelMappingAutomationSettings(
  settings?: Partial<ModelMappingAutomationSettings> | null
): ModelMappingAutomationSettings {
  const seen = new Set<string>()
  const rules: ModelMappingAutoRule[] = []
  for (const rawRule of settings?.rules || []) {
    const rule = normalizeAutoDiscoveredRuleDirection(rawRule)
    const from = normalizeName(rule.from || '')
    const to = normalizeName(rule.to || '')
    if (!from || !to) continue
    const key = `${from}\u0000${to}`
    if (seen.has(key)) continue
    seen.add(key)
    rules.push({
      enabled: rule.enabled !== false,
      from,
      to,
      source: normalizeName(rule.source || ''),
      updated_at: normalizeName(rule.updated_at || '')
    })
  }

  const rawConcurrency = Number(settings?.batch_test_concurrency)
  const batch_test_concurrency =
    Number.isFinite(rawConcurrency) && rawConcurrency >= 1
      ? Math.min(MAX_BATCH_TEST_CONCURRENCY, Math.floor(rawConcurrency))
      : DEFAULT_BATCH_TEST_CONCURRENCY

  return {
    rules,
    batch_test_concurrency
  }
}

function mappingKey(mapping: ModelMappingEntry) {
  return `${normalizeName(mapping.from)}\u0000${normalizeName(mapping.to)}`
}

function buildExistingIndexes(models: string[], mappings: ModelMappingEntry[]) {
  const modelNames = new Set(models.map(model => normalizeName(model)).filter(Boolean))
  const unprefixedModelNamesLower = new Set(
    Array.from(modelNames)
      .filter(model => !model.includes('/'))
      .map(model => model.toLowerCase())
  )
  const mappingFromNames = new Set<string>()
  const mappingPairs = new Set<string>()

  for (const mapping of mappings) {
    const from = normalizeName(mapping.from)
    const to = normalizeName(mapping.to)
    if (!from || !to) continue
    mappingFromNames.add(from)
    mappingPairs.add(mappingKey({ from, to }))
  }

  return {
    modelNames,
    unprefixedModelNamesLower,
    mappingFromNames,
    mappingPairs
  }
}

function appendSuggestion(
  suggestions: ModelMappingSuggestion[],
  seenPairs: Set<string>,
  suggestion: ModelMappingSuggestion
) {
  const from = normalizeName(suggestion.from)
  const to = normalizeName(suggestion.to)
  if (!from || !to || from === to) return
  const key = mappingKey({ from, to })
  if (seenPairs.has(key)) return
  seenPairs.add(key)
  suggestions.push({ ...suggestion, from, to })
}

export function suggestRuleBasedModelMappings(
  models: string[],
  mappings: ModelMappingEntry[],
  rules: ModelMappingAutoRule[]
): ModelMappingSuggestion[] {
  const indexes = buildExistingIndexes(models, mappings)
  const suggestions: ModelMappingSuggestion[] = []
  const seenPairs = new Set(indexes.mappingPairs)
  const seenFrom = new Set(indexes.mappingFromNames)

  for (const rule of rules) {
    if (rule.enabled === false) continue
    const from = normalizeName(rule.from)
    const to = normalizeName(rule.to)
    if (!from || !to) continue
    if (!indexes.modelNames.has(to)) continue
    if (indexes.modelNames.has(from)) continue
    if (seenFrom.has(from)) continue
    appendSuggestion(suggestions, seenPairs, { from, to, reason: 'rule' })
    seenFrom.add(from)
  }

  return suggestions
}

export function discoverModelMappingSuggestions(
  models: string[],
  mappings: ModelMappingEntry[]
): ModelMappingSuggestion[] {
  const indexes = buildExistingIndexes(models, mappings)
  const suggestions: ModelMappingSuggestion[] = []
  const seenPairs = new Set(indexes.mappingPairs)
  const seenFrom = new Set(indexes.mappingFromNames)

  for (const rawModel of models) {
    const model = normalizeName(rawModel)
    if (!model) continue

    const suggestion = deriveAutoMappingSuggestion(model)
    if (!suggestion) continue
    if (indexes.modelNames.has(suggestion.from)) continue
    if (
      suggestion.reason === 'provider_prefix' &&
      indexes.unprefixedModelNamesLower.has(suggestion.from.toLowerCase())
    ) continue
    if (seenFrom.has(suggestion.from)) continue

    appendSuggestion(suggestions, seenPairs, suggestion)
    seenFrom.add(suggestion.from)
  }

  return suggestions
}

export function repairLegacyReversedAutoMappings(
  models: string[],
  mappings: ModelMappingEntry[]
): {
  mappings: ModelMappingEntry[]
  repairedMappings: ModelMappingEntry[]
  repairedCount: number
} {
  const correctionByLegacyPair = new Map<string, ModelMappingSuggestion>()
  for (const model of models) {
    const suggestion = deriveAutoMappingSuggestion(model)
    if (!suggestion) continue
    correctionByLegacyPair.set(
      mappingKey({ from: suggestion.to, to: suggestion.from }),
      suggestion
    )
  }

  const preserved: ModelMappingEntry[] = []
  const repairedMappings: ModelMappingEntry[] = []
  let repairedCount = 0
  for (const mapping of mappings) {
    const correction = correctionByLegacyPair.get(mappingKey(mapping))
    if (!correction) {
      preserved.push(mapping)
      continue
    }
    repairedCount += 1
    repairedMappings.push({ from: correction.from, to: correction.to })
  }

  return {
    mappings: mergeModelMappings(preserved, repairedMappings),
    repairedMappings,
    repairedCount
  }
}

export function mergeModelMappings(
  mappings: ModelMappingEntry[],
  suggestions: ModelMappingEntry[]
): ModelMappingEntry[] {
  const merged: ModelMappingEntry[] = []
  const seenFrom = new Set<string>()

  for (const mapping of mappings) {
    const from = normalizeName(mapping.from)
    const to = normalizeName(mapping.to)
    if (!from || !to) continue
    const fromKey = from
    if (seenFrom.has(fromKey)) continue
    seenFrom.add(fromKey)
    merged.push({ from, to })
  }

  for (const suggestion of suggestions) {
    const from = normalizeName(suggestion.from)
    const to = normalizeName(suggestion.to)
    if (!from || !to) continue
    const fromKey = from
    if (seenFrom.has(fromKey)) continue
    seenFrom.add(fromKey)
    merged.push({ from, to })
  }

  return merged
}

export function mergeAutoRulesFromMappings(
  rules: ModelMappingAutoRule[],
  mappings: ModelMappingEntry[],
  source = 'auto_discovered',
  updatedAt = new Date().toISOString()
): ModelMappingAutoRule[] {
  const normalized = normalizeModelMappingAutomationSettings({ rules, batch_test_concurrency: 3 }).rules
  const seen = new Set(normalized.map(rule => mappingKey(rule)))
  const merged = [...normalized]

  for (const mapping of mappings) {
    const from = normalizeName(mapping.from)
    const to = normalizeName(mapping.to)
    if (!from || !to) continue
    const key = mappingKey({ from, to })
    if (seen.has(key)) continue
    seen.add(key)
    merged.push({
      enabled: true,
      from,
      to,
      source,
      updated_at: updatedAt
    })
  }

  return merged
}

export function isImageGenerationModel(model: string, platform?: string): boolean {
  const normalized = normalizeName(model).toLowerCase().replace(/^models\//, '')
  if (!normalized) return false

  const leaf = normalized.slice(normalized.lastIndexOf('/') + 1)
  const candidates = leaf === normalized ? [normalized] : [normalized, leaf]

  return candidates.some(candidate => {
    if (/^gemini-(?:3\.1-flash|3-pro|2\.5-flash)-image(?:-|$)/.test(candidate)) {
      return true
    }

    if (/^(gpt-image-|dall-e-|imagen-|flux[-._]|midjourney-|mj-|seedream-|jimeng-|kolors-)/.test(candidate)) {
      return true
    }

    if (/^grok-imagine(?:$|-edit$|-image(?:-|$))/.test(candidate)) {
      return true
    }

    if (/(^|[-_/])(imagegen|imggen|stable-diffusion|sdxl)([-_/]|$)/.test(candidate)) {
      return true
    }

    return platform === 'openai' && /^gpt-image-/.test(candidate)
  })
}
