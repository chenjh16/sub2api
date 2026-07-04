import { describe, expect, it } from 'vitest'
import {
  discoverModelMappingSuggestions,
  isImageGenerationModel,
  mergeAutoRulesFromMappings,
  mergeModelMappings,
  normalizeModelMappingAutomationSettings,
  suggestRuleBasedModelMappings
} from '../modelMappingAutomation'

describe('modelMappingAutomation', () => {
  it('discovers mixed-case models and maps them to lowercase names', () => {
    const suggestions = discoverModelMappingSuggestions(['DeepSeek-V4-Pro', 'gpt-5.5'], [])

    expect(suggestions).toEqual([
      { from: 'DeepSeek-V4-Pro', to: 'deepseek-v4-pro', reason: 'lowercase' }
    ])
  })

  it('discovers provider-prefixed models only when the short name is absent', () => {
    expect(discoverModelMappingSuggestions(['deepseek/deepseek-v4-flash'], [])).toEqual([
      {
        from: 'deepseek/deepseek-v4-flash',
        to: 'deepseek-v4-flash',
        reason: 'provider_prefix'
      }
    ])

    expect(discoverModelMappingSuggestions(
      ['deepseek/deepseek-v4-flash', 'deepseek-v4-flash'],
      []
    )).toEqual([])
  })

  it('prefers provider-prefix mapping over whole-name lowercasing for mixed-case prefixed models', () => {
    expect(discoverModelMappingSuggestions(['DeepSeek/DeepSeek-V4-Flash'], [])).toEqual([
      {
        from: 'DeepSeek/DeepSeek-V4-Flash',
        to: 'deepseek-v4-flash',
        reason: 'provider_prefix'
      }
    ])
  })

  it('skips provider-prefix suggestions when the short name already appears in mappings', () => {
    const suggestions = discoverModelMappingSuggestions(
      ['deepseek/deepseek-v4-flash'],
      [{ from: 'alias', to: 'deepseek-v4-flash' }]
    )

    expect(suggestions).toEqual([])
  })

  it('applies enabled global rules against the current model list', () => {
    const suggestions = suggestRuleBasedModelMappings(
      ['DeepSeek-V4-Pro'],
      [],
      [
        { enabled: true, from: 'DeepSeek-V4-Pro', to: 'deepseek-v4-pro' },
        { enabled: false, from: 'ignored', to: 'target' }
      ]
    )

    expect(suggestions).toEqual([
      { from: 'DeepSeek-V4-Pro', to: 'deepseek-v4-pro', reason: 'rule' }
    ])
  })

  it('merges mappings by source model without replacing existing user entries', () => {
    const merged = mergeModelMappings(
      [{ from: 'DeepSeek-V4-Pro', to: 'custom-target' }],
      [{ from: 'DeepSeek-V4-Pro', to: 'deepseek-v4-pro' }, { from: 'Qwen3-Max', to: 'qwen3-max' }]
    )

    expect(merged).toEqual([
      { from: 'DeepSeek-V4-Pro', to: 'custom-target' },
      { from: 'Qwen3-Max', to: 'qwen3-max' }
    ])
  })

  it('normalizes and appends discovered mappings to the global whitelist', () => {
    const rules = mergeAutoRulesFromMappings(
      [{ enabled: true, from: ' existing ', to: 'target' }],
      [{ from: 'DeepSeek-V4-Pro', to: 'deepseek-v4-pro' }],
      'auto_discovered',
      '2026-07-02T00:00:00Z'
    )

    expect(rules).toEqual([
      { enabled: true, from: 'existing', to: 'target', source: '', updated_at: '' },
      {
        enabled: true,
        from: 'DeepSeek-V4-Pro',
        to: 'deepseek-v4-pro',
        source: 'auto_discovered',
        updated_at: '2026-07-02T00:00:00Z'
      }
    ])
  })

  it('normalizes settings and clamps batch concurrency', () => {
    expect(normalizeModelMappingAutomationSettings({
      batch_test_concurrency: 99,
      rules: [{ enabled: true, from: ' a ', to: ' b ' }]
    })).toEqual({
      batch_test_concurrency: 10,
      rules: [{ enabled: true, from: 'a', to: 'b', source: '', updated_at: '' }]
    })
  })

  it('recognizes common image generation model names', () => {
    expect(isImageGenerationModel('gemini-3.1-flash-image')).toBe(true)
    expect(isImageGenerationModel('gpt-image-2')).toBe(true)
    expect(isImageGenerationModel('flux-kontext-pro')).toBe(true)
    expect(isImageGenerationModel('stable-diffusion-xl')).toBe(true)
    expect(isImageGenerationModel('gemini-3.5-flash-thinking')).toBe(false)
    expect(isImageGenerationModel('gpt-5.5')).toBe(false)
  })
})
