<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
            {{ t('admin.sites.title') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500">
            {{ t('admin.sites.description') }}
          </p>
        </div>
        <div class="flex gap-2">
          <button class="btn btn-secondary" :disabled="busy" @click="load">
            {{ t('common.refresh') }}</button
          ><button class="btn btn-primary" @click="editSite()">
            {{ t('admin.sites.add') }}
          </button>
        </div>
      </div>
      <p class="text-sm text-gray-500">
        {{ t('admin.siteBalance.automaticHint') }}
        <RouterLink to="/admin/settings?tab=email" class="text-primary-600">{{ t('admin.siteBalance.emailSettings') }}</RouterLink>
      </p>
      <div class="flex flex-wrap gap-x-6 gap-y-1 text-sm text-gray-500">
        <span v-for="stat in stats" :key="stat.label">{{ stat.label }} <strong class="ml-1 text-gray-900 dark:text-white">{{ stat.value }}</strong></span>
      </div>
      <div
        v-if="loading && !sites.length"
        class="p-10 text-center text-gray-500"
      >
        {{ t('common.loading') }}
      </div>
      <div
        v-else-if="!sites.length"
        class="rounded-xl border border-dashed border-gray-300 p-14 text-center dark:border-dark-600"
      >
        <h2 class="font-medium">{{ t('admin.sites.empty') }}</h2>
        <p class="mx-auto mt-2 max-w-lg text-sm text-gray-500">
          {{ t('admin.sites.emptyHint') }}
        </p>
        <button class="btn btn-primary mt-5" @click="editSite()">
          {{ t('admin.sites.add') }}
        </button>
      </div>
      <div
        v-else
        class="space-y-4"
      >
        <div class="flex gap-3 overflow-x-auto pb-1">
          <button
            v-for="site in sites"
            :key="site.id"
            class="w-60 shrink-0 rounded-xl border bg-white px-4 py-3 text-left transition dark:bg-dark-800"
            :class="
              selectedId === site.id
                ? 'border-primary-500 ring-1 ring-primary-500'
                : 'border-gray-200 dark:border-dark-700'
            "
            @click="selectedId = site.id"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="font-semibold">{{ site.name }}</span
              ><span
                class="rounded bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-700"
                >{{ site.kind === 'kongfang' ? t('admin.sites.kongfang') : site.kind === 'sub2api' ? 'Sub2API' : 'New API' }}</span
              >
            </div>
            <div class="mt-1 truncate text-xs text-gray-500">
              {{ site.base_url }}
            </div>
            <div class="mt-2 flex items-center justify-between text-xs">
              <span
                :class="
                  site.status === 'connected' && site.enabled
                    ? 'text-green-600'
                    : 'text-amber-600'
                "
                >{{
                  t(
                    `admin.sites.status.${site.enabled ? site.status : 'disabled'}`,
                  )
                }}</span
              ><span class="text-gray-500">{{
                t('admin.sites.bindingCount', { count: site.bindings.length })
              }}</span>
            </div>
          </button>
        </div>
        <section
          v-if="selected"
          class="min-w-0 rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
        >
          <div class="border-b border-gray-200 p-5 dark:border-dark-700">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <h2 class="text-lg font-semibold">{{ selected.name }}</h2>
                <p class="mt-1 text-xs text-gray-500">
                  {{ t('admin.sites.lastSync') }}
                  {{ date(selected.last_success) }}
                </p>
              </div>
              <div class="flex flex-wrap gap-2">
                <button
                  class="btn btn-secondary"
                  :disabled="busy"
                  @click="syncSite(selected)"
                >
                  {{ t('admin.sites.sync') }}</button
                ><button
                  class="btn btn-secondary"
                  :disabled="busy"
                  @click="editSite(selected)"
                >
                  {{ t('common.edit') }}</button
                ><button
                  class="btn btn-primary"
                  :disabled="busy || !selected.models.length"
                  @click="openBinding()"
                >
                  {{ t('admin.sites.bind') }}</button
                ><button
                  class="btn btn-danger"
                  :disabled="busy || selected.bindings.length > 0"
                  @click="confirmAction = { kind: 'site', id: selected.id }"
                >
                  {{ t('common.delete') }}
                </button>
              </div>
            </div>
            <p
              v-if="selected.error"
              class="mt-3 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
              role="alert"
            >
              {{ selected.error }}
            </p>
            <SiteBalanceCard :site="selected" :settings="balanceSettings" :disabled="busy" @updated="replace" @busy="busy = $event" />
            <div class="mt-5 flex gap-5">
              <button
                v-for="tab in tabs"
                :key="tab"
                class="border-b-2 pb-2 text-sm"
                :class="
                  activeTab === tab
                    ? 'border-primary-500 text-primary-600'
                    : 'border-transparent text-gray-500'
                "
                @click="activeTab = tab"
              >
                {{ t(`admin.sites.${tab}`) }}
              </button>
            </div>
          </div>
          <div v-if="activeTab === 'bindings'" class="overflow-x-auto">
            <p v-if="!selected.bindings.length" class="p-8 text-center text-sm text-gray-500">{{ t('admin.sites.noBindings') }}</p>
            <table v-else class="w-full text-left text-sm" data-testid="binding-table">
              <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/40">
                <tr>
                  <th class="px-5 py-3">{{ t('admin.sites.upstreamModel') }} / {{ t('admin.sites.upstreamGroup') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.localGroup') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.priceAndEligibility') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.scheduling') }}</th>
                  <th class="px-5 py-3 text-right">{{ t('common.actions') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="binding in selected.bindings" :key="binding.id" data-testid="binding-row">
                  <td class="max-w-64 px-5 py-3">
                    <div class="truncate font-medium" :title="binding.model">{{ binding.model }}</div>
                    <div class="mt-1 truncate text-xs text-gray-500" :title="bindingModel(binding)?.group_name">{{ bindingModel(binding)?.group_name || binding.group_id }} · #{{ binding.account_id || '—' }}</div>
                  </td>
                  <td class="max-w-52 px-3 py-3">
                    <div class="truncate" :title="groupName(binding.local_group_id)">{{ groupName(binding.local_group_id) }}</div>
                    <div v-if="binding.local_model !== binding.model" class="mt-1 truncate text-xs text-gray-500">{{ binding.local_model }}</div>
                  </td>
                  <td class="px-3 py-3"><SitePriceSummary :tiers="bindingTiers(binding)" /></td>
                  <td class="px-3 py-3 whitespace-nowrap">
                    <span :class="bindingStatus(binding) === 'ready' ? 'text-green-600' : 'text-amber-700'">{{ t(`admin.sites.status.${bindingStatus(binding)}`) }}</span>
                    <div v-if="bindingReason(binding)" class="mt-1 text-xs text-gray-500" :title="binding.error || (binding.status === 'preview' ? t('admin.sites.previewPaused') : '')">{{ binding.error || t(`admin.sites.status.${bindingReason(binding)}`) }}</div>
                  </td>
                  <td class="px-5 py-3 text-right whitespace-nowrap">
                    <button class="text-primary-600" :disabled="busy" @click="openBinding(binding)">{{ t('admin.sites.details') }}</button>
                    <button class="ml-3 text-red-600" :disabled="busy" @click="confirmAction = { kind: 'binding', id: binding.id }">{{ t('admin.sites.unbind') }}</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else-if="activeTab === 'models'" class="p-5">
            <input
              v-model="modelSearch"
              class="input mb-4"
              :placeholder="t('admin.sites.searchModels')"
            />
            <div class="max-h-[600px] space-y-2 overflow-auto">
              <div
                v-for="model in filteredModels"
                :key="`${model.group_id}:${model.model}`"
                class="flex items-start justify-between gap-3 rounded-lg border border-gray-100 p-3 dark:border-dark-700"
              >
                <div class="min-w-0">
                  <div class="break-all text-sm font-medium">
                    {{ model.model }}
                  </div>
                  <div class="mt-1 text-xs text-gray-500">
                    {{ model.group_name }} · {{ model.platform }}
                  </div>
                  <div v-if="model.reason" class="mt-1 text-xs text-amber-700">
                    {{ model.reason }}
                  </div>
                  <div
                    v-for="tier in model.tiers"
                    :key="tier.key"
                    class="mt-1 text-xs text-gray-500"
                  >
                    {{ tier.key }} · {{ prices(tier.prices) }} · {{ tier.unit }}
                    <div v-if="tier.note" class="text-amber-600">
                      {{ tier.note }}
                    </div>
                  </div>
                </div>
                <button
                  class="shrink-0 text-sm text-primary-600"
                  :disabled="busy || !model.tiers.length"
                  @click="openBinding(undefined, model)"
                >
                  {{ t('admin.sites.bind') }}
                </button>
              </div>
            </div>
          </div>
          <div v-else class="space-y-3 p-5">
            <p
              v-if="!selected.history.length"
              class="py-10 text-center text-sm text-gray-500"
            >
              {{ t('admin.sites.noChanges') }}
            </p>
            <div
              v-for="(change, index) in selected.history"
              :key="index"
              class="rounded-lg border border-gray-100 p-3 text-sm dark:border-dark-700"
            >
              <div class="font-medium">
                {{ change.model }}
                <span class="ml-2 text-xs font-normal text-gray-500">{{
                  date(change.at)
                }}</span>
              </div>
              <div class="mt-1 text-xs text-gray-500">
                {{
                  change.before
                    .map((p) => `${p.key}: ${prices(p.prices)}`)
                    .join(' / ')
                }}
                →
                {{
                  change.after
                    .map((p) => `${p.key}: ${prices(p.prices)}`)
                    .join(' / ')
                }}
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>
    <BaseDialog
      :show="siteDialog"
      :title="t(editingId ? 'admin.sites.edit' : 'admin.sites.add')"
      @close="closeSite"
    >
      <form id="site-form" class="space-y-4" @submit.prevent="saveSite">
        <label class="block text-sm"
          >{{ t('admin.sites.name')
          }}<input
            v-model="siteForm.name"
            class="input mt-1"
            required
            maxlength="120"
        /></label>
        <label class="block text-sm"
          >{{ t('admin.sites.url')
          }}<input
            v-model="siteForm.base_url"
            class="input mt-1"
            type="url"
            placeholder="https://api.example.com"
            required
            :disabled="identityLocked"
        /></label>
        <div class="grid grid-cols-2 gap-4">
          <label class="text-sm"
            >{{ t('admin.sites.format')
            }}<select
              v-model="siteForm.kind"
              class="input mt-1"
              :disabled="identityLocked"
            >
              <option value="sub2api">Sub2API</option>
              <option value="newapi">New API</option>
              <option value="kongfang">{{ t('admin.sites.kongfang') }}</option>
            </select></label
          ><label class="text-sm"
            >{{ t('admin.sites.auth')
            }}<select
              v-model="siteForm.auth_mode"
              class="input mt-1"
              :disabled="identityLocked"
            >
              <option value="password">
                {{ t('admin.sites.passwordLogin') }}
              </option>
              <option value="token">{{ siteForm.kind === 'kongfang' ? 'Access Token' : 'Access / Refresh Token' }}</option>
            </select></label
          >
        </div>
        <template v-if="siteForm.auth_mode === 'password'"
          ><label class="block text-sm"
            >{{ t('admin.sites.username')
            }}<input
              v-model="siteForm.username"
              class="input mt-1"
              autocomplete="off"
              :required="!editingId"
              :disabled="identityLocked" /></label
          ><label class="block text-sm"
            >{{ t('admin.sites.password')
            }}<input
              v-model="siteForm.password"
              class="input mt-1"
              type="password"
              autocomplete="new-password"
              :required="!editingId"
              :placeholder="
                editingId ? t('admin.sites.keepSecret') : ''
              " /></label
        ></template>
        <template v-else
          ><label class="block text-sm"
            >Access Token<textarea
              v-model="siteForm.access_token"
              class="input mt-1 font-mono text-xs"
              rows="3"
              autocomplete="off"
              :placeholder="editingId ? t('admin.sites.keepSecret') : ''"
            /></label
          ><label v-if="siteForm.kind !== 'kongfang'" class="block text-sm"
            >Refresh Token<input
              v-model="siteForm.refresh_token"
              class="input mt-1"
              type="password"
              autocomplete="new-password"
              :placeholder="
                editingId ? t('admin.sites.keepSecret') : ''
              " /></label
          ><label v-if="siteForm.kind === 'newapi'" class="block text-sm"
            >{{ t('admin.sites.userId')
            }}<input
              v-model.number="siteForm.user_id"
              class="input mt-1"
              type="number"
              min="0"
              :disabled="identityLocked" /></label
        ></template>
        <p class="text-xs text-gray-500">{{ t(siteForm.kind === 'kongfang' ? 'admin.sites.kongfangAuthHint' : 'admin.sites.authHint') }}</p>
        <label v-if="siteForm.kind === 'kongfang'" class="block text-sm">
          {{ t('admin.sites.usdPerCredit') }}
          <input v-model.number="siteForm.usd_per_credit" data-testid="usd-per-credit" class="input mt-1" type="number" min="0" step="any" />
          <span class="mt-1 block text-xs text-gray-500">{{ t('admin.sites.usdPerCreditHint') }}</span>
        </label>
        <label class="flex items-center gap-2 text-sm"
          ><input v-model="siteForm.enabled" type="checkbox" />{{
            t('admin.sites.enabled')
          }}</label
        >
      </form>
      <template #footer
        ><button class="btn btn-secondary" :disabled="busy" @click="closeSite">
          {{ t('common.cancel') }}</button
        ><button
          class="btn btn-primary"
          form="site-form"
          type="submit"
          :disabled="busy"
        >
          {{ t('admin.sites.saveConnect') }}
        </button></template
      >
    </BaseDialog>
    <BaseDialog
      :show="bindingDialog"
      :title="t('admin.sites.bind')"
      width="wide"
      @close="!busy && (bindingDialog = false)"
    >
      <form id="binding-form" class="space-y-4" @submit.prevent="saveBinding">
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="text-sm"
            >{{ t('admin.sites.upstreamGroup')
            }}<select
              v-model="bindingForm.group_id"
              class="input mt-1"
              required
              :disabled="!!bindingForm.id"
              @change="clearUpstreamModel"
            >
              <option value="" disabled>{{ t('admin.sites.choose') }}</option>
              <option
                v-for="group in upstreamGroups"
                :key="group.id"
                :value="group.id"
              >
                {{ group.name }}
              </option>
            </select></label
          ><label class="text-sm"
            >{{ t('admin.sites.upstreamModel')
            }}<select
              v-model="bindingForm.model"
              class="input mt-1"
              required
              :disabled="!!bindingForm.id"
              @change="initLimits"
            >
              <option value="" disabled>{{ t('admin.sites.choose') }}</option>
              <option
                v-for="model in upstreamModels"
                :key="model.model"
                :value="model.model"
              >
                {{ model.model }}
              </option>
            </select></label
          >
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="text-sm"
            >{{ t('admin.sites.localGroup')
            }}<select
              v-model.number="bindingForm.local_group_id"
              class="input mt-1"
              required
              :disabled="!!bindingForm.id"
              @change="loadLocalModels"
            >
              <option :value="0" disabled>{{ t('admin.sites.choose') }}</option>
              <option
                v-for="group in compatibleGroups"
                :key="group.id"
                :value="group.id"
              >
                {{ group.name }} · {{ group.platform }}
              </option>
            </select></label
          ><label class="text-sm"
            >{{ t('admin.sites.localModel')
            }}<input
              v-model="bindingForm.local_model"
              class="input mt-1"
              list="site-local-models"
              required
              :disabled="!!bindingForm.id" /><datalist id="site-local-models">
              <option
                v-for="model in localModels"
                :key="model"
                :value="model"
              /></datalist
          ></label>
        </div>
        <p
          class="rounded-lg bg-primary-50 p-3 text-xs text-primary-800 dark:bg-primary-900/20 dark:text-primary-200"
        >
          {{ t('admin.sites.limitHint') }}
        </p>
        <p v-if="chosenModel?.reason" class="text-sm text-amber-700">
          {{ chosenModel.reason }}
        </p>
        <p v-if="pricingLoading" class="text-sm text-gray-500">
          {{ t('admin.sites.pricingLoading') }}
        </p>
        <p v-if="pricingError" class="text-sm text-amber-700">{{ pricingError }}</p>
        <div
          v-for="limit in bindingForm.limits"
          :key="limit.key"
          class="rounded-lg border border-gray-200 p-3 dark:border-dark-700"
        >
          <label class="flex items-center gap-2 text-sm font-medium"
            ><input v-model="limit.enabled" type="checkbox" />{{
              limit.key === 'default' ? t('admin.sites.defaultTier') : limit.key
            }}
            <span class="font-normal text-gray-500">{{
              limit.unit
            }}</span></label
          >
          <p v-if="limit.reason" class="mt-2 text-xs text-amber-700">
            {{ limit.reason }}
          </p>
          <div class="mt-3 grid gap-3 sm:grid-cols-3 text-xs">
            <div>
              <span class="text-gray-500">{{ t('admin.sites.price') }}</span>
              <p class="mt-1">{{ prices(bindingForm.price_tiers?.find((p) => p.key === limit.key)?.prices || {}) }}</p>
            </div>
            <div>
              <span class="text-gray-500">{{ t('admin.sites.selling') }}</span>
              <p class="mt-1">{{ prices(limit.selling || {}) }}</p>
            </div>
            <div>
              <span class="text-gray-500">{{ t('admin.sites.limit') }}</span>
              <p class="mt-1 font-medium" data-testid="auto-limit">{{ prices(limit.limits) }}</p>
            </div>
          </div>
        </div>
        <label class="flex items-center gap-2 text-sm"
          ><input v-model="bindingForm.enabled" type="checkbox" />{{
            t('admin.sites.bindingEnabled')
          }}</label
        >
      </form>
      <template #footer
        ><button
          class="btn btn-secondary"
          :disabled="busy"
          @click="bindingDialog = false"
        >
          {{ t('common.cancel') }}</button
        ><button
          class="btn btn-primary"
          form="binding-form"
          type="submit"
          :disabled="
            busy ||
            pricingLoading ||
            !!pricingError ||
            !bindingForm.local_group_id ||
            !bindingForm.local_model
          "
        >
          {{ t('common.save') }}
        </button></template
      >
    </BaseDialog>
    <BaseDialog
      :show="!!confirmAction"
      :title="t('common.delete')"
      @close="!busy && (confirmAction = null)"
      ><p class="text-sm">
        {{
          t(
            confirmAction?.kind === 'site'
              ? 'admin.sites.deleteConfirm'
              : 'admin.sites.unbindConfirm',
          )
        }}
      </p>
      <template #footer
        ><button
          class="btn btn-secondary"
          :disabled="busy"
          @click="confirmAction = null"
        >
          {{ t('common.cancel') }}</button
        ><button
          class="btn btn-danger"
          :disabled="busy"
          @click="removeConfirmed"
        >
          {{ t('common.confirm') }}
        </button></template
      ></BaseDialog
    >
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import SiteBalanceCard from '@/components/admin/sites/SiteBalanceCard.vue'
import SitePriceSummary from '@/components/admin/sites/SitePriceSummary.vue'
import { upstreamSiteBalanceApi, type SiteBalanceSettings } from '@/api/admin/upstreamSiteBalance'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { getAll, getModelAllowlistCandidates } from '@/api/admin/groups'
import {
  upstreamSitesApi,
  siteTierStatus,
  type UpstreamSite,
  type SiteInput,
  type SiteBinding,
  type SiteModel,
  type SitePriceTier,
} from '@/api/admin/upstreamSites'
import type { AdminGroup } from '@/types'
const { t } = useI18n()
const app = useAppStore()
const route = useRoute()
const balanceSettings = ref<SiteBalanceSettings | null>(null)
const sites = ref<UpstreamSite[]>([])
const groups = ref<AdminGroup[]>([])
const selectedId = ref(String(route.query.site || ''))
const loading = ref(false)
const busy = ref(false)
const activeTab = ref('bindings')
const tabs = ['bindings', 'models', 'history']
const modelSearch = ref('')
const selected = computed(() =>
  sites.value.find((s) => s.id === selectedId.value),
)
const stats = computed(() => [
  { label: t('admin.sites.totalSites'), value: sites.value.length },
  {
    label: t('admin.sites.totalBindings'),
    value: sites.value.reduce((n, s) => n + s.bindings.length, 0),
  },
  { label: t('admin.sites.syncInterval'), value: t('admin.sites.fiveMinutes') },
])
const filteredModels = computed(
  () =>
    selected.value?.models.filter((m) =>
      `${m.model} ${m.group_name}`
        .toLowerCase()
        .includes(modelSearch.value.toLowerCase()),
    ) || [],
)
const date = (value?: string) =>
  value ? new Date(value).toLocaleString() : '—'
const prices = (values: Record<string, number>) =>
  Object.entries(values)
    .map(
      ([k, v]) =>
        `${t(`admin.sites.components.${k}`)} $${Number(v.toPrecision(8))}`,
    )
    .join(' / ') || '—'
const groupName = (id: number) =>
  groups.value.find((g) => g.id === id)?.name || `#${id}`
const bindingModel = (b: SiteBinding) =>
  selected.value?.models.find(
    (m) => m.group_id === b.group_id && m.model === b.model,
  )
const tierStatus = (b: SiteBinding, tier: SitePriceTier) => {
  const status = siteTierStatus(
    {
      site_id: selected.value!.id,
      site_name: selected.value!.name,
      binding_id: b.id,
      enabled: selected.value!.enabled && b.enabled,
      fresh_until: selected.value!.last_success
        ? new Date(
            Date.parse(selected.value!.last_success) + 600000,
          ).toISOString()
        : '',
      tiers: b.price_tiers || bindingModel(b)?.tiers || [],
      limits: b.limits,
      reason: bindingModel(b)?.reason,
    },
    tier,
  )
  return status === 'ready' && b.status === 'preview' ? 'preview' : status
}
const bindingTiers = (b: SiteBinding) => (b.price_tiers || bindingModel(b)?.tiers || []).map(tier => ({
  ...tier, status: tierStatus(b, tier),
  selling: b.limits.find(limit => limit.key === tier.key)?.selling,
  ceiling: b.limits.find(limit => limit.key === tier.key)?.limits,
}))
const bindingStatus = (b: SiteBinding) => {
  const tiers = bindingTiers(b)
  const allowed = tiers.filter(tier => tier.status === 'ready').length
  return allowed === 0 || b.status === 'error' ? 'blocked' : allowed === tiers.length ? 'ready' : 'partial'
}
const bindingReason = (b: SiteBinding) => b.status === 'error' ? 'error' : bindingTiers(b).find(tier => tier.status !== 'ready')?.status || ''
function error(e: unknown) {
  app.showError(
    e instanceof Error
      ? e.message
      : (e as { message?: string })?.message || t('admin.sites.failed'),
  )
}
function replace(site: UpstreamSite) {
  const i = sites.value.findIndex((s) => s.id === site.id)
  if (i < 0) sites.value.push(site)
  else sites.value[i] = site
  selectedId.value = site.id
}
async function load() {
  loading.value = true
  try {
    sites.value = await upstreamSitesApi.list()
    if (!sites.value.some((s) => s.id === selectedId.value))
      selectedId.value = sites.value[0]?.id || ''
  } catch (e) {
    error(e)
  } finally {
    loading.value = false
  }
}
async function syncSite(site: UpstreamSite) {
  busy.value = true
  try {
    const result = await upstreamSitesApi.sync(site.id)
    replace(result)
    if (result.error) app.showError(result.error)
    else app.showSuccess(t('admin.sites.synced'))
  } catch (e) {
    error(e)
  } finally {
    busy.value = false
  }
}
const emptySite = (): SiteInput => ({
  usd_per_credit: 0,
  name: '',
  base_url: '',
  kind: 'sub2api',
  auth_mode: 'password',
  username: '',
  user_id: 0,
  enabled: true,
  password: '',
  access_token: '',
  refresh_token: '',
})
const siteDialog = ref(false)
const editingId = ref('')
const siteForm = ref<SiteInput>(emptySite())
const identityLocked = computed(
  () => !!sites.value.find((s) => s.id === editingId.value)?.bindings.length,
)
function editSite(site?: UpstreamSite) {
  editingId.value = site?.id || ''
  siteForm.value = site
    ? {
        ...emptySite(),
        usd_per_credit: site.usd_per_credit || 0,
        name: site.name,
        base_url: site.base_url,
        kind: site.kind,
        auth_mode: site.auth_mode,
        username: site.username,
        user_id: site.user_id,
        enabled: site.enabled,
      }
    : emptySite()
  siteDialog.value = true
}
function closeSite() {
  if (!busy.value) {
    siteDialog.value = false
    siteForm.value = emptySite()
  }
}
async function saveSite() {
  busy.value = true
  try {
    const result = await upstreamSitesApi.save(editingId.value, { ...siteForm.value, usd_per_credit: Number(siteForm.value.usd_per_credit) || 0, refresh_token: siteForm.value.kind === 'kongfang' ? '' : siteForm.value.refresh_token })
    replace(result)
    siteDialog.value = false
    siteForm.value = emptySite()
    const synced = await upstreamSitesApi.sync(result.id)
    replace(synced)
    if (synced.error) app.showError(synced.error)
    else app.showSuccess(t('admin.sites.synced'))
  } catch (e) {
    error(e)
  } finally {
    busy.value = false
  }
}
const emptyBinding = (): SiteBinding => ({
  id: '',
  group_id: '',
  model: '',
  local_group_id: 0,
  local_model: '',
  platform: '',
  account_id: 0,
  enabled: true,
  limits: [],
  status: '',
})
const bindingDialog = ref(false)
const bindingForm = ref<SiteBinding>(emptyBinding())
const localModels = ref<string[]>([])
const upstreamGroups = computed(() => [
  ...new Map(
    (selected.value?.models || []).map((m) => [
      m.group_id,
      { id: m.group_id, name: m.group_name },
    ]),
  ).values(),
])
const upstreamModels = computed(
  () =>
    selected.value?.models.filter(
      (m) => m.group_id === bindingForm.value.group_id,
    ) || [],
)
const chosenModel = computed(() =>
  upstreamModels.value.find((m) => m.model === bindingForm.value.model),
)
const compatibleGroups = computed(() =>
  groups.value.filter(
    (g) =>
      ['openai', 'anthropic', 'gemini'].includes(g.platform) &&
      (!['sub2api', 'kongfang'].includes(selected.value?.kind || '') ||
        !chosenModel.value?.platform ||
        chosenModel.value.platform === g.platform),
  ),
)
function clearUpstreamModel() {
  bindingForm.value.model = ''
  bindingForm.value.limits = []
}
function initLimits() {
  bindingForm.value.limits = []
  if (!bindingForm.value.local_model)
    bindingForm.value.local_model = bindingForm.value.model
}
function openBinding(binding?: SiteBinding, model?: SiteModel) {
  bindingForm.value = binding
    ? JSON.parse(JSON.stringify(binding))
    : emptyBinding()
  if (model) {
    bindingForm.value.group_id = model.group_id
    bindingForm.value.model = model.model
    initLimits()
  }
  bindingDialog.value = true
  if (binding) {
    void loadLocalModels()
  }
}
const pricingLoading = ref(false)
const pricingError = ref('')
let pricingRequest = 0
async function refreshPricePreview() {
  const request = ++pricingRequest
  pricingError.value = ''
  if (
    !bindingDialog.value ||
    !selected.value ||
    !bindingForm.value.local_group_id ||
    !bindingForm.value.local_model ||
    !bindingForm.value.model
  ) {
    pricingLoading.value = false
    bindingForm.value.limits = []
    return
  }
  pricingLoading.value = true
  try {
    const result = await upstreamSitesApi.pricePreview(
      selected.value.id,
      bindingForm.value,
    )
    if (request !== pricingRequest) return
    // Keep checkbox changes made while the preview was loading.
    for (const limit of result.limits) {
      const current = bindingForm.value.limits.find((l) => l.key === limit.key)
      if (current) limit.enabled = current.enabled
    }
    bindingForm.value.limits = result.limits
    bindingForm.value.price_tiers = result.price_tiers
  } catch {
    if (request === pricingRequest) {
      bindingForm.value.limits = []
      pricingError.value = t('admin.sites.pricingFailed')
    }
  } finally {
    if (request === pricingRequest) pricingLoading.value = false
  }
}
watch(
  () => [
    bindingDialog.value,
    bindingForm.value.group_id,
    bindingForm.value.model,
    bindingForm.value.local_group_id,
    bindingForm.value.local_model,
  ],
  refreshPricePreview,
)
async function loadLocalModels() {
  const id = bindingForm.value.local_group_id
  if (!id) {
    localModels.value = []
    return
  }
  try {
    const models = await getModelAllowlistCandidates(id)
    if (id === bindingForm.value.local_group_id) localModels.value = models
  } catch (e) {
    error(e)
  }
}
async function saveBinding() {
  if (!selected.value || pricingLoading.value || pricingError.value) return
  busy.value = true
  try {
    const result = await upstreamSitesApi.bind(
      selected.value.id,
      {
        ...bindingForm.value,
        price_tiers: undefined,
        limits: bindingForm.value.limits.map((l) => ({
          key: l.key,
          unit: l.unit,
          enabled: l.enabled,
          limits: {},
        })),
      },
    )
    replace(result)
    const binding = result.bindings.find(
      (b) =>
        b.group_id === bindingForm.value.group_id &&
        b.model === bindingForm.value.model &&
        b.local_group_id === bindingForm.value.local_group_id &&
        b.local_model === bindingForm.value.local_model,
    )
    if (binding?.error) {
      bindingForm.value = JSON.parse(JSON.stringify(binding))
      app.showError(binding.error)
    } else {
      bindingDialog.value = false
      app.showSuccess(t('admin.sites.bound'))
    }
  } catch (e) {
    error(e)
  } finally {
    busy.value = false
  }
}
const confirmAction = ref<{ kind: 'site' | 'binding'; id: string } | null>(null)
async function removeConfirmed() {
  if (!confirmAction.value) return
  busy.value = true
  try {
    if (confirmAction.value.kind === 'site') {
      await upstreamSitesApi.remove(confirmAction.value.id)
      await load()
    } else if (selected.value)
      replace(
        await upstreamSitesApi.unbind(
          selected.value.id,
          confirmAction.value.id,
        ),
      )
    confirmAction.value = null
  } catch (e) {
    error(e)
  } finally {
    busy.value = false
  }
}
let refreshTimer: ReturnType<typeof setInterval> | undefined
onMounted(async () => {
  await load()
  try {
    groups.value = await getAll()
    balanceSettings.value = await upstreamSiteBalanceApi.settings()
  } catch (e) {
    error(e)
  }
  refreshTimer = setInterval(() => {
    if (!busy.value && !siteDialog.value && !bindingDialog.value) void load()
  }, 30000)
})
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  siteForm.value = emptySite()
})
</script>
