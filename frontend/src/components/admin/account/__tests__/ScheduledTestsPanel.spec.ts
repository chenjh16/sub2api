import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { createPlanMock, listPlansMock } = vi.hoisted(() => ({
  createPlanMock: vi.fn(),
  listPlansMock: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    scheduledTests: {
      listByAccount: listPlansMock,
      create: createPlanMock,
      update: vi.fn(),
      delete: vi.fn(),
      listResults: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (!params) return key
        return `${key}:${JSON.stringify(params)}`
      }
    })
  }
})

import ScheduledTestsPanel from '../ScheduledTestsPanel.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /></div>'
})

const InputStub = defineComponent({
  name: 'Input',
  props: {
    modelValue: {
      type: [String, Number],
      default: ''
    }
  },
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
})

const ToggleStub = defineComponent({
  name: 'Toggle',
  props: {
    modelValue: {
      type: Boolean,
      default: false
    }
  },
  emits: ['update:modelValue'],
  template: '<button type="button" @click="$emit(\'update:modelValue\', !modelValue)">{{ modelValue }}</button>'
})

const SelectStub = defineComponent({
  name: 'Select',
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option></select>'
})

function mountPanel() {
  return mount(ScheduledTestsPanel, {
    props: {
      show: true,
      accountId: 12,
      modelOptions: [
        { value: 'agnes-image-2.1-flash', label: 'agnes-image-2.1-flash' },
        { value: 'agnes-1.5-flash', label: 'agnes-1.5-flash' },
        { value: 'deepseek-v4-pro', label: 'deepseek-v4-pro' }
      ]
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        HelpTooltip: { template: '<span><slot name="trigger" /><slot /></span>' },
        Input: InputStub,
        Toggle: ToggleStub,
        Select: SelectStub,
        Icon: true,
        Teleport: true,
        Transition: false
      }
    }
  })
}

describe('ScheduledTestsPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listPlansMock.mockResolvedValue([])
    createPlanMock.mockImplementation((req) =>
      Promise.resolve({
        id: Math.floor(Math.random() * 1000),
        ...req,
        last_run_at: null,
        next_run_at: null,
        created_at: '',
        updated_at: ''
      })
    )
  })

  it('filters models and creates one plan for each selected model', async () => {
    const wrapper = mountPanel()

    await wrapper.get('button').trigger('click')
    await wrapper.get('[data-testid="scheduled-test-model-search"]').setValue('agnes')

    const visibleOptions = wrapper.findAll('[data-testid="scheduled-test-model-option"]')
    expect(visibleOptions).toHaveLength(2)
    expect(visibleOptions.map((option) => option.text())).toEqual([
      'agnes-image-2.1-flash',
      'agnes-1.5-flash'
    ])

    await wrapper.get('[data-testid="scheduled-test-select-filtered-models"]').trigger('click')
    await wrapper.findAll('input').at(1)!.setValue('*/30 * * * *')
    await wrapper.get('[data-testid="scheduled-test-save-new-plan"]').trigger('click')
    await flushPromises()

    expect(createPlanMock).toHaveBeenCalledTimes(2)
    expect(createPlanMock).toHaveBeenNthCalledWith(1, expect.objectContaining({
      account_id: 12,
      model_id: 'agnes-image-2.1-flash',
      cron_expression: '*/30 * * * *'
    }))
    expect(createPlanMock).toHaveBeenNthCalledWith(2, expect.objectContaining({
      account_id: 12,
      model_id: 'agnes-1.5-flash',
      cron_expression: '*/30 * * * *'
    }))
  })
})
