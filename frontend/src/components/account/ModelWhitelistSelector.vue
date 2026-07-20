<template>
  <div>
    <!-- Multi-select Dropdown -->
    <div class="relative mb-3">
      <div
        @click="toggleDropdown"
        class="cursor-pointer rounded-lg border border-gray-300 bg-white px-3 py-2 dark:border-dark-500 dark:bg-dark-700"
      >
        <div class="grid grid-cols-2 gap-1.5">
          <span
            v-for="model in modelValue"
            :key="model"
            :class="[
              'inline-flex items-center gap-1 rounded px-2 py-1 text-xs transition-colors',
              enableModelSelection && !isModelEnabled(model)
                ? 'bg-gray-50 text-gray-400 dark:bg-dark-700 dark:text-gray-500'
                : 'bg-gray-100 text-gray-700 dark:bg-dark-600 dark:text-gray-300'
            ]"
          >
            <span class="flex min-w-0 flex-1 items-center gap-1 truncate">
              <ModelIcon :model="model" size="14px" />
              <span class="truncate">{{ model }}</span>
            </span>
            <span class="ml-auto inline-flex shrink-0 items-center justify-end gap-0.5">
              <button
                v-if="canTestModels"
                type="button"
                @click.stop="testSingleModel(model)"
                :title="t('admin.accounts.testThisModel')"
                :class="[
                  'shrink-0 rounded-full p-0.5 transition-colors hover:bg-gray-200 dark:hover:bg-dark-500',
                  modelTestButtonClass(model)
                ]"
              >
                <Icon
                  :name="modelTestResults[model]?.status === 'running' ? 'refresh' : modelTestIcon(model)"
                  size="xs"
                  :class="{ 'animate-spin': modelTestResults[model]?.status === 'running' }"
                  :stroke-width="2"
                />
              </button>
              <button
                type="button"
                @click.stop="removeModel(model)"
                :title="t('admin.accounts.removeModel')"
                :aria-label="t('admin.accounts.removeModel')"
                class="shrink-0 rounded-full hover:bg-gray-200 dark:hover:bg-dark-500"
              >
                <Icon name="x" size="xs" class="h-3.5 w-3.5" :stroke-width="2" />
              </button>
              <button
                v-if="enableModelSelection"
                type="button"
                @click.stop="toggleModelEnabled(model)"
                :title="modelEnableTitle(model)"
                :class="[
                  'shrink-0 rounded-full p-0.5 transition-colors hover:bg-gray-200 dark:hover:bg-dark-500',
                  isModelEnabled(model)
                    ? 'text-emerald-600 dark:text-emerald-400'
                    : 'text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300'
                ]"
              >
                <Icon
                  :name="isModelEnabled(model) ? 'checkCircle' : 'circle'"
                  size="sm"
                  :stroke-width="2.15"
                />
              </button>
            </span>
          </span>
        </div>
        <div class="mt-2 flex items-center justify-between border-t border-gray-200 pt-2 dark:border-dark-600">
          <span class="text-xs text-gray-400">{{ modelCountLabel }}</span>
          <svg class="h-5 w-5 text-gray-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
          </svg>
        </div>
      </div>
      <!-- Dropdown List -->
      <div
        v-if="showDropdown"
        class="absolute left-0 right-0 top-full z-50 mt-1 rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-700"
      >
        <div class="sticky top-0 border-b border-gray-200 bg-white p-2 dark:border-dark-600 dark:bg-dark-700">
          <input
            v-model="searchQuery"
            type="text"
            class="input w-full text-sm"
            :placeholder="t('admin.accounts.searchModels')"
            @click.stop
          />
        </div>
        <div class="max-h-52 overflow-auto">
          <button
            v-for="model in filteredModels"
            :key="model.value"
            type="button"
            @click="toggleModel(model.value)"
            class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-600"
          >
            <span
              :class="[
                'flex h-4 w-4 shrink-0 items-center justify-center rounded border',
                modelValue.includes(model.value)
                  ? 'border-primary-500 bg-primary-500 text-white'
                  : 'border-gray-300 dark:border-dark-500'
              ]"
            >
              <svg v-if="modelValue.includes(model.value)" class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="3" d="M5 13l4 4L19 7" />
              </svg>
            </span>
            <ModelIcon :model="model.value" size="18px" />
            <span class="truncate text-gray-900 dark:text-white">{{ model.value }}</span>
          </button>
          <div v-if="filteredModels.length === 0" class="px-3 py-4 text-center text-sm text-gray-500">
            {{ t('admin.accounts.noMatchingModels') }}
          </div>
        </div>
      </div>
    </div>

    <!-- Quick Actions -->
    <div class="mb-4 flex flex-wrap gap-2">
      <button
        type="button"
        @click="fillRelated"
        class="rounded-lg border border-blue-200 px-3 py-1.5 text-sm text-blue-600 hover:bg-blue-50 dark:border-blue-800 dark:text-blue-400 dark:hover:bg-blue-900/30"
      >
        {{ t('admin.accounts.fillRelatedModels') }}
      </button>
      <button
        v-if="canSyncUpstream"
        type="button"
        @click="syncUpstreamModels"
        :disabled="isSyncingUpstream"
        class="rounded-lg border border-emerald-200 px-3 py-1.5 text-sm text-emerald-600 hover:bg-emerald-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-emerald-800 dark:text-emerald-400 dark:hover:bg-emerald-900/30"
      >
        {{ isSyncingUpstream ? t('admin.accounts.syncUpstreamModelsLoading') : t('admin.accounts.syncUpstreamModels') }}
      </button>
      <button
        v-if="enableMappingTools"
        type="button"
        @click="applyRuleMappings"
        :disabled="isApplyingMapping"
        class="rounded-lg border border-purple-200 px-3 py-1.5 text-sm text-purple-600 hover:bg-purple-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-purple-800 dark:text-purple-400 dark:hover:bg-purple-900/30"
      >
        {{ t('admin.accounts.autoRuleMapModels') }}
      </button>
      <button
        v-if="enableMappingTools"
        type="button"
        @click="applyAutoMappings"
        :disabled="isApplyingMapping"
        class="rounded-lg border border-indigo-200 px-3 py-1.5 text-sm text-indigo-600 hover:bg-indigo-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-indigo-800 dark:text-indigo-400 dark:hover:bg-indigo-900/30"
      >
        {{ t('admin.accounts.autoMapModels') }}
      </button>
      <button
        v-if="canTestModels"
        type="button"
        @click="startBatchTest"
        :disabled="modelValue.length === 0"
        class="inline-flex items-center gap-1.5 rounded-lg border border-amber-200 px-3 py-1.5 text-sm text-amber-600 hover:bg-amber-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-800 dark:text-amber-400 dark:hover:bg-amber-900/30"
      >
        <Icon
          :name="isBatchTesting ? 'refresh' : batchTestIcon"
          size="sm"
          :class="{ 'animate-spin': isBatchTesting }"
          :stroke-width="2"
        />
        {{ batchTestLabel }}
      </button>
      <button
        v-if="enableModelSelection && modelValue.length > 0"
        type="button"
        @click="enableAllCandidateModels"
        class="rounded-lg border border-primary-200 px-3 py-1.5 text-sm text-primary-600 hover:bg-primary-50 dark:border-primary-800 dark:text-primary-400 dark:hover:bg-primary-900/30"
      >
        {{ t('admin.accounts.enableAllModels') }}
      </button>
      <button
        v-if="enableModelSelection && modelValue.length > 0"
        type="button"
        @click="disableAllCandidateModels"
        class="rounded-lg border border-gray-200 px-3 py-1.5 text-sm text-gray-600 hover:bg-gray-50 dark:border-dark-500 dark:text-gray-300 dark:hover:bg-dark-600"
      >
        {{ t('admin.accounts.disableAllModels') }}
      </button>
      <button
        type="button"
        @click="clearAll"
        class="rounded-lg border border-red-200 px-3 py-1.5 text-sm text-red-600 hover:bg-red-50 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-900/30"
      >
        {{ t('admin.accounts.clearAllModels') }}
      </button>
    </div>

    <div
      v-if="showAutoRefreshSettings"
      class="mb-4 rounded-lg border border-primary-100 bg-primary-50/60 p-3 dark:border-primary-900/50 dark:bg-primary-900/10"
    >
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <div class="text-sm font-medium text-gray-800 dark:text-gray-100">
            {{ t('admin.accounts.modelAutoRefreshTitle') }}
          </div>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.modelAutoRefreshDescription') }}
          </p>
        </div>
        <button
          type="button"
          class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors"
          :class="autoRefreshEnabledValue ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'"
          :title="t('admin.accounts.modelAutoRefreshToggle')"
          @click="setAutoRefreshEnabled(!autoRefreshEnabledValue)"
        >
          <span
            class="inline-block h-5 w-5 rounded-full bg-white shadow transition-transform"
            :class="autoRefreshEnabledValue ? 'translate-x-5' : 'translate-x-0.5'"
          />
        </button>
      </div>

      <div v-if="autoRefreshEnabledValue" class="mt-3 grid gap-3 md:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300">
            {{ t('admin.accounts.modelAutoRefreshInterval') }}
          </span>
          <input
            type="number"
            min="10"
            max="43200"
            step="10"
            class="input w-full text-sm"
            :value="autoRefreshIntervalValue"
            @input="setAutoRefreshIntervalFromEvent"
          />
          <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.modelAutoRefreshIntervalHint') }}
          </span>
        </label>

        <div class="flex items-start gap-3 rounded-lg bg-white/70 p-3 dark:bg-dark-700/50">
          <button
            type="button"
            class="relative mt-0.5 inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors"
            :class="autoRefreshTestFilterValue ? 'bg-emerald-600' : 'bg-gray-200 dark:bg-dark-600'"
            :title="t('admin.accounts.modelAutoRefreshTestFilter')"
            @click="setAutoRefreshTestFilter(!autoRefreshTestFilterValue)"
          >
            <span
              class="inline-block h-5 w-5 rounded-full bg-white shadow transition-transform"
              :class="autoRefreshTestFilterValue ? 'translate-x-5' : 'translate-x-0.5'"
            />
          </button>
          <div class="min-w-0">
            <div class="text-sm font-medium text-gray-700 dark:text-gray-200">
              {{ t('admin.accounts.modelAutoRefreshTestFilter') }}
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.modelAutoRefreshTestFilterHint') }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <!-- Custom Model Input -->
    <div class="mb-3">
      <label class="mb-1.5 block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.accounts.customModelName') }}</label>
      <div class="flex gap-2">
        <input
          v-model="customModel"
          type="text"
          class="input flex-1"
          :placeholder="t('admin.accounts.enterCustomModelName')"
          @keydown.enter.prevent="handleEnter"
          @compositionstart="isComposing = true"
          @compositionend="isComposing = false"
        />
        <button
          type="button"
          @click="addCustom"
          class="rounded-lg bg-primary-50 px-4 py-2 text-sm font-medium text-primary-600 hover:bg-primary-100 dark:bg-primary-900/30 dark:text-primary-400 dark:hover:bg-primary-900/50"
        >
          {{ t('admin.accounts.addModel') }}
        </button>
      </div>
    </div>

    <Teleport to="body">
      <BaseDialog
        :show="showModelTestPanel"
        :title="t('admin.accounts.modelBatchTestResults')"
        width="wide"
        @close="showModelTestPanel = false"
      >
        <div class="space-y-4">
          <div class="grid grid-cols-3 gap-3">
            <div class="rounded-lg bg-gray-50 p-3 text-center dark:bg-dark-700">
              <div class="text-lg font-semibold text-gray-900 dark:text-gray-100">{{ modelTestSummary.total }}</div>
              <div class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.modelTestTotal') }}</div>
            </div>
            <div class="rounded-lg bg-green-50 p-3 text-center dark:bg-green-900/20">
              <div class="text-lg font-semibold text-green-700 dark:text-green-300">{{ modelTestSummary.success }}</div>
              <div class="text-xs text-green-600 dark:text-green-400">{{ t('admin.accounts.modelTestSuccess') }}</div>
            </div>
            <div class="rounded-lg bg-red-50 p-3 text-center dark:bg-red-900/20">
              <div class="text-lg font-semibold text-red-700 dark:text-red-300">{{ modelTestSummary.failed }}</div>
              <div class="text-xs text-red-600 dark:text-red-400">{{ t('admin.accounts.modelTestFailed') }}</div>
            </div>
          </div>
          <div class="max-h-[55vh] overflow-auto rounded-lg border border-gray-200 dark:border-dark-600">
            <div
              v-for="model in modelValue"
              :key="'test-result-' + model"
              class="grid grid-cols-[minmax(0,1fr)_110px_90px] items-start gap-3 border-b border-gray-100 px-3 py-2 text-sm last:border-b-0 dark:border-dark-600"
            >
              <div class="min-w-0">
                <div class="truncate font-medium text-gray-800 dark:text-gray-100">{{ model }}</div>
                <div class="mt-1 line-clamp-2 text-xs text-gray-500 dark:text-gray-400">
                  {{ modelTestResults[model]?.message || t('admin.accounts.modelTestNotRun') }}
                </div>
              </div>
              <span :class="['rounded-full px-2 py-1 text-center text-xs font-medium', modelTestStatusClass(model)]">
                {{ modelTestStatusLabel(model) }}
              </span>
              <span class="text-right text-xs text-gray-500 dark:text-gray-400">
                {{ modelTestResults[model]?.duration_ms ? `${modelTestResults[model]?.duration_ms}ms` : '-' }}
              </span>
            </div>
          </div>
        </div>
      </BaseDialog>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { accountsAPI } from '@/api/admin/accounts'
import { settingsAPI } from '@/api/admin/settings'
import type { SyncUpstreamPreviewParams } from '@/api/admin/accounts'
import { buildApiUrl } from '@/api/client'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ModelIcon from '@/components/common/ModelIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { allModels, getModelsByPlatform, normalizeModelList } from '@/composables/useModelWhitelist'
import {
  discoverModelMappingSuggestions,
  mergeAutoRulesFromMappings,
  mergeModelMappings,
  normalizeModelMappingAutomationSettings,
  repairLegacyReversedAutoMappings,
  suggestRuleBasedModelMappings,
  type ModelMappingAutomationSettings,
  type ModelMappingEntry
} from '@/utils/modelMappingAutomation'

const { t, locale } = useI18n()

const props = defineProps<{
  modelValue: string[]
  platform?: string
  platforms?: string[]
  accountId?: number
  syncCredentials?: {
    platform: string
    type: string
    base_url?: string
    api_key: string
  }
  modelMappings?: ModelMappingEntry[]
  enabledModels?: string[]
  enableMappingTools?: boolean
  enableModelTesting?: boolean
  enableModelSelection?: boolean
  enableAutoRefreshSettings?: boolean
  autoRefreshEnabled?: boolean
  autoRefreshIntervalMinutes?: number
  autoRefreshTestFilterEnabled?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
  'update:modelMappings': [value: ModelMappingEntry[]]
  'update:enabledModels': [value: string[]]
  'update:autoRefreshEnabled': [value: boolean]
  'update:autoRefreshIntervalMinutes': [value: number]
  'update:autoRefreshTestFilterEnabled': [value: boolean]
}>()

const appStore = useAppStore()

const showDropdown = ref(false)
const searchQuery = ref('')
const customModel = ref('')
const isComposing = ref(false)
const isSyncingUpstream = ref(false)
const isApplyingMapping = ref(false)
const isBatchTesting = ref(false)
const showModelTestPanel = ref(false)
const mappingAutomationSettings = ref<ModelMappingAutomationSettings>(
  normalizeModelMappingAutomationSettings()
)

type ModelTestStatus = 'idle' | 'running' | 'success' | 'error'

interface ModelTestResult {
  status: ModelTestStatus
  message: string
  duration_ms?: number
}

const modelTestResults = reactive<Record<string, ModelTestResult>>({})

const normalizedPlatforms = computed(() => {
  const rawPlatforms =
    props.platforms && props.platforms.length > 0
      ? props.platforms
      : props.platform
        ? [props.platform]
        : []

  return Array.from(
    new Set(
      rawPlatforms
        .map(platform => platform?.trim())
        .filter((platform): platform is string => Boolean(platform))
    )
  )
})

const upstreamSyncPlatforms = new Set(['anthropic', 'openai', 'gemini', 'antigravity', 'grok'])
const canSyncUpstream = computed(() => {
  if (props.accountId) {
    if (normalizedPlatforms.value.length === 0) return true
    return normalizedPlatforms.value.some(platform => upstreamSyncPlatforms.has(platform.toLowerCase()))
  }
  if (props.syncCredentials) {
    return upstreamSyncPlatforms.has(props.syncCredentials.platform.toLowerCase())
  }
  return false
})

const enableMappingTools = computed(() => props.enableMappingTools === true && Array.isArray(props.modelMappings))
const canTestModels = computed(() => props.enableModelTesting !== false && Boolean(props.accountId))
const enableModelSelection = computed(() => props.enableModelSelection === true)
const showAutoRefreshSettings = computed(() => props.enableAutoRefreshSettings === true && canSyncUpstream.value)
const autoRefreshEnabledValue = computed(() => props.autoRefreshEnabled === true)
const autoRefreshTestFilterValue = computed(() => props.autoRefreshTestFilterEnabled === true)
const autoRefreshIntervalValue = computed(() => normalizeAutoRefreshInterval(props.autoRefreshIntervalMinutes))

const currentModelMappings = computed(() => props.modelMappings || [])
const enabledModelList = computed(() =>
  enableModelSelection.value
    ? normalizeModelList(props.enabledModels || [])
    : normalizeModelList(props.modelValue)
)
const enabledModelSet = computed(() => new Set(enabledModelList.value))
const modelCountLabel = computed(() =>
  enableModelSelection.value
    ? t('admin.accounts.enabledModelCount', {
        enabled: enabledModelList.value.length,
        total: props.modelValue.length
      })
    : t('admin.accounts.modelCount', { count: props.modelValue.length })
)

const modelTestSummary = computed(() => {
  let success = 0
  let failed = 0
  let running = 0
  for (const model of props.modelValue) {
    const status = modelTestResults[model]?.status
    if (status === 'success') success += 1
    if (status === 'error') failed += 1
    if (status === 'running') running += 1
  }
  return {
    total: props.modelValue.length,
    success,
    failed,
    running
  }
})

const batchTestIcon = computed(() => {
  if (modelTestSummary.value.failed > 0) return 'xCircle'
  if (modelTestSummary.value.success > 0 && modelTestSummary.value.success === modelTestSummary.value.total) {
    return 'checkCircle'
  }
  return 'beaker'
})

const batchTestLabel = computed(() => {
  if (isBatchTesting.value) return t('admin.accounts.batchTestingModels')
  const { success, failed, total } = modelTestSummary.value
  if (success > 0 || failed > 0) {
    return t('admin.accounts.batchTestModelsDone', { success, failed, total })
  }
  return t('admin.accounts.batchTestModels')
})

const availableOptions = computed(() => {
  if (normalizedPlatforms.value.length === 0) {
    return allModels
  }

  const allowedModels = new Set<string>()
  for (const platform of normalizedPlatforms.value) {
    for (const model of getModelsByPlatform(platform)) {
      allowedModels.add(model)
    }
  }

  return allModels.filter(model => allowedModels.has(model.value))
})

const filteredModels = computed(() => {
  const query = searchQuery.value.toLowerCase().trim()
  if (!query) return availableOptions.value
  return availableOptions.value.filter(
    m => m.value.toLowerCase().includes(query) || m.label.toLowerCase().includes(query)
  )
})

const loadMappingAutomationSettings = async () => {
  try {
    mappingAutomationSettings.value = normalizeModelMappingAutomationSettings(
      await settingsAPI.getModelMappingAutomationSettings()
    )
  } catch {
    mappingAutomationSettings.value = normalizeModelMappingAutomationSettings()
    appStore.showError(t('admin.accounts.autoMappingSettingsLoadFailed'))
  }
}

const normalizeAutoRefreshInterval = (value?: number) => {
  const raw = Number(value)
  if (!Number.isFinite(raw)) return 1440
  return Math.min(43200, Math.max(10, Math.floor(raw)))
}

const setAutoRefreshEnabled = (enabled: boolean) => {
  emit('update:autoRefreshEnabled', enabled)
}

const setAutoRefreshInterval = (value: string | number) => {
  emit('update:autoRefreshIntervalMinutes', normalizeAutoRefreshInterval(Number(value)))
}

const setAutoRefreshIntervalFromEvent = (event: Event) => {
  const input = event.target as HTMLInputElement | null
  setAutoRefreshInterval(input?.value || '')
}

const setAutoRefreshTestFilter = (enabled: boolean) => {
  emit('update:autoRefreshTestFilterEnabled', enabled)
}

onMounted(() => {
  if (enableMappingTools.value || canTestModels.value) {
    void loadMappingAutomationSettings()
  }
})

const toggleDropdown = () => {
  showDropdown.value = !showDropdown.value
  if (!showDropdown.value) searchQuery.value = ''
}

const emitModels = (models: string[], enabledModels = enabledModelList.value) => {
  const nextModels = normalizeModelList(models)
  emit('update:modelValue', nextModels)
  if (enableModelSelection.value) {
    const candidateSet = new Set(nextModels)
    emit('update:enabledModels', normalizeModelList(enabledModels).filter(model => candidateSet.has(model)))
  }
}

const addCandidateModels = (models: string[], shouldEnableNewModels: boolean) => {
  const existingModels = normalizeModelList(props.modelValue)
  const existingSet = new Set(existingModels)
  const nextModels = [...existingModels]
  const nextEnabled = normalizeModelList(enabledModelList.value)
  const enabledSet = new Set(nextEnabled)

  for (const model of normalizeModelList(models)) {
    if (!existingSet.has(model)) {
      existingSet.add(model)
      nextModels.push(model)
    }
    if (shouldEnableNewModels && !enabledSet.has(model)) {
      enabledSet.add(model)
      nextEnabled.push(model)
    }
  }

  emitModels(nextModels, nextEnabled)
}

const isModelEnabled = (model: string) => {
  return !enableModelSelection.value || enabledModelSet.value.has(model)
}

const modelEnableTitle = (model: string) => {
  return isModelEnabled(model)
    ? t('admin.accounts.disableModel')
    : t('admin.accounts.enableModel')
}

const removeModel = (model: string) => {
  emitModels(
    props.modelValue.filter(m => m !== model),
    enabledModelList.value.filter(m => m !== model)
  )
}

const toggleModel = (model: string) => {
  if (props.modelValue.includes(model)) {
    removeModel(model)
  } else {
    addCandidateModels([model], true)
  }
}

const toggleModelEnabled = (model: string) => {
  if (!enableModelSelection.value) return
  if (enabledModelSet.value.has(model)) {
    emitModels(props.modelValue, enabledModelList.value.filter(m => m !== model))
    return
  }
  emitModels(props.modelValue, [...enabledModelList.value, model])
}

const enableAllCandidateModels = () => {
  emitModels(props.modelValue, props.modelValue)
}

const disableAllCandidateModels = () => {
  emitModels(props.modelValue, [])
}

const addCustom = () => {
  const model = customModel.value.trim()
  if (!model) return
  if (props.modelValue.includes(model)) {
    appStore.showInfo(t('admin.accounts.modelExists'))
    return
  }
  addCandidateModels([model], true)
  customModel.value = ''
}

const handleEnter = () => {
  if (!isComposing.value) addCustom()
}

const fillRelated = () => {
  const newModels: string[] = []
  for (const platform of normalizedPlatforms.value) {
    for (const model of getModelsByPlatform(platform)) {
      newModels.push(model)
    }
  }
  addCandidateModels(newModels, true)
}

const updateModelMappings = (mappings: ModelMappingEntry[]) => {
  emit('update:modelMappings', mappings)
}

const applyMappingSuggestions = async (
  suggestions: ModelMappingEntry[],
  successKey: 'admin.accounts.autoMapAdded' | 'admin.accounts.autoRuleMapAdded',
  shouldPersistRules: boolean,
  baseMappings: ModelMappingEntry[] = currentModelMappings.value,
  repairedMappings: ModelMappingEntry[] = [],
  repairedCount = 0
) => {
  const normalizedBase = mergeModelMappings(baseMappings, [])
  const merged = mergeModelMappings(normalizedBase, suggestions)
  const added = merged.length - normalizedBase.length
  const changed = added + repairedCount
  if (changed <= 0) {
    appStore.showInfo(t('admin.accounts.autoMapNoChanges'))
    return
  }

  updateModelMappings(merged)
  appStore.showSuccess(t(successKey, { count: changed }))

  if (shouldPersistRules) {
    const nextSettings = {
      ...mappingAutomationSettings.value,
      rules: mergeAutoRulesFromMappings(
        mappingAutomationSettings.value.rules,
        [...repairedMappings, ...suggestions]
      )
    }
    try {
      mappingAutomationSettings.value = normalizeModelMappingAutomationSettings(
        await settingsAPI.updateModelMappingAutomationSettings(nextSettings)
      )
    } catch {
      appStore.showError(t('admin.accounts.autoMappingSettingsSaveFailed'))
    }
  }
}

const applyRuleMappings = async () => {
  if (!enableMappingTools.value || isApplyingMapping.value) return
  if (props.modelValue.length === 0) {
    appStore.showInfo(t('admin.accounts.autoMapNoModels'))
    return
  }

  isApplyingMapping.value = true
  try {
    await loadMappingAutomationSettings()
    const repaired = repairLegacyReversedAutoMappings(
      props.modelValue,
      currentModelMappings.value
    )
    const suggestions = suggestRuleBasedModelMappings(
      props.modelValue,
      repaired.mappings,
      mappingAutomationSettings.value.rules
    )
    await applyMappingSuggestions(
      suggestions,
      'admin.accounts.autoRuleMapAdded',
      false,
      repaired.mappings,
      repaired.repairedMappings,
      repaired.repairedCount
    )
  } finally {
    isApplyingMapping.value = false
  }
}

const applyAutoMappings = async () => {
  if (!enableMappingTools.value || isApplyingMapping.value) return
  if (props.modelValue.length === 0) {
    appStore.showInfo(t('admin.accounts.autoMapNoModels'))
    return
  }

  isApplyingMapping.value = true
  try {
    await loadMappingAutomationSettings()
    const repaired = repairLegacyReversedAutoMappings(
      props.modelValue,
      currentModelMappings.value
    )
    const ruleSuggestions = suggestRuleBasedModelMappings(
      props.modelValue,
      repaired.mappings,
      mappingAutomationSettings.value.rules
    )
    const afterRules = mergeModelMappings(repaired.mappings, ruleSuggestions)
    const discoveredSuggestions = discoverModelMappingSuggestions(props.modelValue, afterRules)
    await applyMappingSuggestions(
      [...ruleSuggestions, ...discoveredSuggestions],
      'admin.accounts.autoMapAdded',
      true,
      repaired.mappings,
      repaired.repairedMappings,
      repaired.repairedCount
    )
  } finally {
    isApplyingMapping.value = false
  }
}

const syncUpstreamModels = async () => {
  if (isSyncingUpstream.value) return
  if (!props.accountId && !props.syncCredentials) return

  isSyncingUpstream.value = true
  try {
    let result
    if (props.accountId) {
      result = await accountsAPI.syncUpstreamModels(props.accountId)
    } else if (props.syncCredentials) {
      result = await accountsAPI.syncUpstreamModelsPreview(props.syncCredentials as SyncUpstreamPreviewParams)
    } else {
      return
    }

    const upstreamModels = result.models.map(model => model.trim()).filter(Boolean)
    if (upstreamModels.length === 0) {
      appStore.showInfo(t('admin.accounts.syncUpstreamModelsEmpty'))
      return
    }

    const newModels = [...props.modelValue]
    let addedCount = 0
    for (const model of upstreamModels) {
      if (!newModels.includes(model)) {
        newModels.push(model)
        addedCount += 1
      }
    }

    emitModels(newModels, enabledModelList.value)
    if (addedCount > 0) {
      appStore.showSuccess(t('admin.accounts.syncUpstreamModelsSuccess', { count: addedCount, total: upstreamModels.length }))
    } else {
      appStore.showInfo(t('admin.accounts.syncUpstreamModelsNoChanges', { count: upstreamModels.length }))
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : t('admin.accounts.syncUpstreamModelsFailed')
    appStore.showError(t('admin.accounts.syncUpstreamModelsError', { message }))
  } finally {
    isSyncingUpstream.value = false
  }
}

const clearAll = () => {
  emitModels([], [])
}

const parseModelTestEvent = (
  event: { type?: string; text?: string; success?: boolean; error?: string; image_url?: string },
  state: { content: string; status: ModelTestStatus; message: string }
) => {
  switch (event.type) {
    case 'content':
      if (event.text) state.content += event.text
      break
    case 'status':
      if (event.text) state.message = event.text
      break
    case 'image':
      if (event.image_url) {
        state.message = t('admin.accounts.modelTestImageReceived')
      }
      break
    case 'test_complete':
      state.status = event.success ? 'success' : 'error'
      state.message = state.content.trim() || event.error || state.message || (
        event.success ? t('admin.accounts.modelTestPassed') : t('admin.accounts.modelTestError')
      )
      break
    case 'error':
      state.status = 'error'
      state.message = event.error || t('admin.accounts.modelTestError')
      break
  }
}

const requestModelTest = async (model: string): Promise<ModelTestResult> => {
  if (!props.accountId) {
    return { status: 'error', message: t('admin.accounts.modelTestRequiresSavedAccount') }
  }

  const startedAt = Date.now()
  const state = {
    content: '',
    status: 'running' as ModelTestStatus,
    message: t('admin.accounts.modelTestRunning')
  }

  try {
    const response = await fetch(buildApiUrl(`/admin/accounts/${props.accountId}/test`), {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${localStorage.getItem('auth_token') || ''}`,
        'Content-Type': 'application/json'
      },
      body: JSON.stringify({
        model_id: model,
        prompt: '',
        mode: 'default',
        locale: String(locale.value || '')
      })
    })

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    const reader = response.body?.getReader()
    if (!reader) {
      throw new Error(t('admin.accounts.modelTestNoResponseBody'))
    }

    const decoder = new TextDecoder()
    let buffer = ''
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const lines = buffer.split('\n')
      buffer = lines.pop() || ''
      for (const line of lines) {
        if (!line.startsWith('data:')) continue
        const jsonText = line.replace(/^data:\s*/, '').trim()
        if (!jsonText) continue
        try {
          parseModelTestEvent(JSON.parse(jsonText), state)
        } catch {
          // Ignore malformed SSE fragments; the final status still comes from complete/error events.
        }
      }
    }

    if (state.status === 'running') {
      state.status = state.content.trim() ? 'success' : 'error'
      state.message = state.content.trim() || t('admin.accounts.modelTestStreamEnded')
    }
  } catch (error) {
    state.status = 'error'
    state.message = error instanceof Error ? error.message : t('admin.accounts.modelTestError')
  }

  return {
    status: state.status,
    message: state.message,
    duration_ms: Date.now() - startedAt
  }
}

const testSingleModel = async (model: string) => {
  if (!canTestModels.value) return
  if (modelTestResults[model]?.status === 'running') {
    showModelTestPanel.value = true
    return
  }
  showModelTestPanel.value = true
  modelTestResults[model] = {
    status: 'running',
    message: t('admin.accounts.modelTestRunning')
  }
  modelTestResults[model] = await requestModelTest(model)
}

const startBatchTest = async () => {
  if (!canTestModels.value || props.modelValue.length === 0) return
  if (isBatchTesting.value) {
    showModelTestPanel.value = true
    return
  }
  await loadMappingAutomationSettings()
  isBatchTesting.value = true
  showModelTestPanel.value = true
  const queue = [...props.modelValue]
  for (const model of queue) {
    modelTestResults[model] = {
      status: 'idle',
      message: t('admin.accounts.modelTestQueued')
    }
  }

  const concurrency = Math.max(1, mappingAutomationSettings.value.batch_test_concurrency || 3)
  let cursor = 0
  const worker = async () => {
    while (cursor < queue.length) {
      const model = queue[cursor++]
      modelTestResults[model] = {
        status: 'running',
        message: t('admin.accounts.modelTestRunning')
      }
      modelTestResults[model] = await requestModelTest(model)
    }
  }

  try {
    await Promise.all(Array.from({ length: Math.min(concurrency, queue.length) }, () => worker()))
  } finally {
    isBatchTesting.value = false
  }
}

const modelTestIcon = (model: string) => {
  const status = modelTestResults[model]?.status
  if (status === 'success') return 'checkCircle'
  if (status === 'error') return 'xCircle'
  return 'beaker'
}

const modelTestButtonClass = (model: string) => {
  const status = modelTestResults[model]?.status
  if (status === 'success') return 'text-green-600 hover:bg-green-50 dark:text-green-400 dark:hover:bg-green-900/20'
  if (status === 'error') return 'text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20'
  return 'text-gray-500 hover:text-primary-600 dark:text-gray-400 dark:hover:text-primary-300'
}

const modelTestStatusClass = (model: string) => {
  const status = modelTestResults[model]?.status
  if (status === 'success') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (status === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (status === 'running') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
}

const modelTestStatusLabel = (model: string) => {
  const status = modelTestResults[model]?.status
  if (status === 'success') return t('admin.accounts.modelTestSuccess')
  if (status === 'error') return t('admin.accounts.modelTestFailed')
  if (status === 'running') return t('admin.accounts.modelTestRunning')
  return t('admin.accounts.modelTestNotRun')
}

</script>
