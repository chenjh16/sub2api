import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

const groupsViewSource = readFileSync(resolve(process.cwd(), 'src/views/admin/GroupsView.vue'), 'utf8')

describe('GroupsView Composite route options', () => {
  it('offers Kimi, Zhipu GLM, and DeepSeek as route targets', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(
      expect.arrayContaining(['kimi', 'zhipu', 'deepseek'])
    )
  })

  it('clears OpenAI-only default service tier when create form switches to Composite', () => {
    expect(groupsViewSource).toMatch(
      /if \(newVal !== "openai"\) \{\s+createForm\.openai_default_service_tier = "";/
    )
  })

  it('clears OpenAI-only default service tier when edit form switches to Composite', () => {
    expect(groupsViewSource).toMatch(
      /if \(newVal !== "openai"\) \{\s+editForm\.openai_default_service_tier = "";/
    )
  })

  it('only renders default service tier controls for concrete OpenAI groups', () => {
    expect(groupsViewSource).toMatch(
      /v-if="createForm\.platform === 'openai'"\s+class="mb-3"\s+data-testid="create-openai-default-service-tier"/
    )
    expect(groupsViewSource).toMatch(
      /v-if="editForm\.platform === 'openai'"\s+class="mb-3"\s+data-testid="edit-openai-default-service-tier"/
    )
  })

  it('offers the upstream ultrafast tier as an OpenAI group default', () => {
    expect(groupsViewSource).toContain('{ value: "ultrafast", label: "ultrafast" }')
  })
})
