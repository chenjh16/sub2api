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

export function normalizeModelMappingAutomationSettings(
  settings?: Partial<ModelMappingAutomationSettings> | null
): ModelMappingAutomationSettings {
  const seen = new Set<string>()
  const rules: ModelMappingAutoRule[] = []
  for (const rule of settings?.rules || []) {
    const from = normalizeName(rule.from || '')
    const to = normalizeName(rule.to || '')
    if (!from || !to) continue
    const key = `${from.toLowerCase()}\u0000${to.toLowerCase()}`
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
  return `${normalizeName(mapping.from).toLowerCase()}\u0000${normalizeName(mapping.to).toLowerCase()}`
}

function buildExistingIndexes(models: string[], mappings: ModelMappingEntry[]) {
  const modelNames = new Set(models.map(model => normalizeName(model)).filter(Boolean))
  const modelNamesLower = new Set(Array.from(modelNames).map(model => model.toLowerCase()))
  const mappingFromLower = new Set<string>()
  const mappingToLower = new Set<string>()
  const mappingPairsLower = new Set<string>()

  for (const mapping of mappings) {
    const from = normalizeName(mapping.from)
    const to = normalizeName(mapping.to)
    if (!from || !to) continue
    mappingFromLower.add(from.toLowerCase())
    mappingToLower.add(to.toLowerCase())
    mappingPairsLower.add(mappingKey({ from, to }))
  }

  return {
    modelNames,
    modelNamesLower,
    mappingFromLower,
    mappingToLower,
    mappingPairsLower
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
  const seenPairs = new Set(indexes.mappingPairsLower)

  for (const rule of rules) {
    if (rule.enabled === false) continue
    const from = normalizeName(rule.from)
    const to = normalizeName(rule.to)
    if (!from || !to) continue
    if (!indexes.modelNamesLower.has(from.toLowerCase())) continue
    if (indexes.mappingFromLower.has(from.toLowerCase())) continue
    appendSuggestion(suggestions, seenPairs, { from, to, reason: 'rule' })
  }

  return suggestions
}

export function discoverModelMappingSuggestions(
  models: string[],
  mappings: ModelMappingEntry[]
): ModelMappingSuggestion[] {
  const indexes = buildExistingIndexes(models, mappings)
  const suggestions: ModelMappingSuggestion[] = []
  const seenPairs = new Set(indexes.mappingPairsLower)

  for (const rawModel of models) {
    const model = normalizeName(rawModel)
    if (!model || indexes.mappingFromLower.has(model.toLowerCase())) continue

    const lower = model.toLowerCase()
    let providerPrefixSuggestionAdded = false

    const slashIndex = model.indexOf('/')
    if (slashIndex > 0 && slashIndex < model.length - 1) {
      const target = normalizeName(model.slice(slashIndex + 1)).toLowerCase()
      const targetLower = target.toLowerCase()
      if (
        target &&
        !target.includes('/') &&
        !indexes.modelNamesLower.has(targetLower) &&
        !indexes.mappingFromLower.has(targetLower) &&
        !indexes.mappingToLower.has(targetLower)
      ) {
        appendSuggestion(suggestions, seenPairs, {
          from: model,
          to: target,
          reason: 'provider_prefix'
        })
        providerPrefixSuggestionAdded = true
      }
    }

    if (!providerPrefixSuggestionAdded && model !== lower && /[A-Z]/.test(model)) {
      appendSuggestion(suggestions, seenPairs, {
        from: model,
        to: lower,
        reason: 'lowercase'
      })
    }
  }

  return suggestions
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
    const fromKey = from.toLowerCase()
    if (seenFrom.has(fromKey)) continue
    seenFrom.add(fromKey)
    merged.push({ from, to })
  }

  for (const suggestion of suggestions) {
    const from = normalizeName(suggestion.from)
    const to = normalizeName(suggestion.to)
    if (!from || !to) continue
    const fromKey = from.toLowerCase()
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

  if (/^gemini-(?:3\.1-flash|3-pro|2\.5-flash)-image(?:-|$)/.test(normalized)) {
    return true
  }

  if (/^(gpt-image-|dall-e-|imagen-|flux-|midjourney-|mj-|seedream-|jimeng-|kolors-)/.test(normalized)) {
    return true
  }

  if (/(^|[-_/])(imagegen|imggen|stable-diffusion|sdxl)([-_/]|$)/.test(normalized)) {
    return true
  }

  return platform === 'openai' && /^gpt-image-/.test(normalized)
}
