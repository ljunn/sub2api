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
        <SiteOverview :sites="sites" :selected-id="selectedId" :threshold="balanceSettings?.threshold ?? 20" :now="now"
          @select="selectSite" @models="openNewModels" />
      </div>
    </div>
    <BaseDialog
      v-if="selected && detailsOpen && !siteDialog && !bindingDialog && !priceModel && !confirmAction"
      key="site-details"
      :show="true"
      :title="selected.name"
      width="extra-wide"
      body-class="!p-0"
      @close="detailsOpen = false"
    >
        <section
          class="min-w-0 [overflow-wrap:anywhere]"
        >
          <div class="border-b border-gray-200 p-4 dark:border-dark-700 sm:p-5">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div>
                <p class="mt-1 text-xs text-gray-500">
                  {{ t('admin.sites.lastSync') }}
                  {{ date(selected.last_success) }}
                </p>
              </div>
              <div class="grid w-full grid-cols-2 gap-2 sm:flex sm:w-auto sm:flex-wrap">
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
            <p v-for="warning in selected.warnings" :key="warning"
              class="mt-3 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300"
              role="status" data-testid="site-sync-warning">{{ warning }}</p>
            <SiteBalanceCard :site="selected" :settings="balanceSettings" :disabled="busy" @updated="replace" @busy="busy = $event" />
            <div class="mt-4 grid grid-cols-3 gap-2 sm:flex sm:gap-5">
              <button
                v-for="tab in tabs"
                :key="tab"
                class="min-h-11 border-b-2 pb-2 text-sm"
                :class="
                  activeTab === tab
                    ? 'border-primary-500 text-primary-600'
                    : 'border-transparent text-gray-500'
                "
                @click="selectTab(tab)"
              >
                {{ t(`admin.sites.${tab}`) }}
                <span v-if="tab === 'models' && selected.models.some(model => model.unread)" class="ml-1 rounded-full bg-blue-50 px-1.5 text-xs text-blue-700">{{ selected.models.filter(model => model.unread).length }}</span>
              </button>
            </div>
          </div>
          <div v-if="activeTab === 'bindings'" class="p-4 sm:p-5 lg:overflow-x-auto lg:p-0">
            <p v-if="!selected.bindings.length" class="p-8 text-center text-sm text-gray-500">{{ t('admin.sites.noBindings') }}</p>
            <table v-else class="block w-full text-left text-sm lg:table" data-testid="binding-table">
              <thead class="hidden bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/40 lg:table-header-group">
                <tr>
                  <th class="px-5 py-3">{{ t('admin.sites.upstreamModel') }} / {{ t('admin.sites.upstreamGroup') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.localGroup') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.priceAndEligibility') }}</th>
                  <th class="px-3 py-3">{{ t('admin.sites.scheduling') }}</th>
                  <th class="px-5 py-3 text-right">{{ t('common.actions') }}</th>
                </tr>
              </thead>
              <tbody class="grid gap-4 lg:table-row-group lg:divide-y lg:divide-gray-100 lg:dark:divide-dark-700">
                <tr v-for="binding in selected.bindings" :key="binding.id" data-testid="binding-row" class="grid min-w-0 gap-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700 lg:table-row lg:rounded-none lg:border-0 lg:p-0">
                  <td class="min-w-0 lg:max-w-64 lg:px-5 lg:py-3">
                    <div class="font-medium [overflow-wrap:anywhere] lg:truncate" :title="binding.model">{{ binding.model }}</div>
                    <div class="mt-1 text-xs text-gray-500 [overflow-wrap:anywhere] lg:truncate" :title="bindingModel(binding)?.group_name">{{ bindingModel(binding)?.group_name || binding.group_id }} · #{{ binding.account_id || '—' }}</div>
                  </td>
                  <td class="min-w-0 lg:max-w-52 lg:px-3 lg:py-3">
                    <span class="mb-1 block text-xs text-gray-500 lg:hidden">{{ t('admin.sites.localGroup') }}</span>
                    <div class="[overflow-wrap:anywhere] lg:truncate" :title="groupName(binding.local_group_id)">{{ groupName(binding.local_group_id) }}</div>
                    <div v-if="binding.local_model !== binding.model" class="mt-1 text-xs text-gray-500 [overflow-wrap:anywhere] lg:truncate">{{ binding.local_model }}</div>
                  </td>
                  <td class="min-w-0 lg:px-3 lg:py-3">
                    <span class="mb-2 block text-xs text-gray-500 lg:hidden">{{ t('admin.sites.priceAndEligibility') }}</span>
                    <SitePriceSummary :tiers="bindingTiers(binding)" />
                    <p v-if="siteModelUsesCachedPrice(selected, bindingModel(binding))" class="mt-1 text-xs text-amber-700" :title="bindingModel(binding)?.price_sync_error || selected.error">{{ t('admin.sites.cachedPrice') }} · {{ date(bindingModel(binding)?.price_updated_at || selected.last_success) }}</p>
                  </td>
                  <td class="min-w-0 lg:px-3 lg:py-3">
                    <span class="mb-1 block text-xs text-gray-500 lg:hidden">{{ t('admin.sites.scheduling') }}</span>
                    <span :class="bindingStatus(binding) === 'ready' ? 'text-green-600' : 'text-amber-700'">{{ t(`admin.sites.status.${bindingStatus(binding)}`) }}</span>
                    <div v-if="binding.traffic_support?.enabled" class="mt-1 text-xs text-primary-600">{{ t('admin.sites.support.target', { percent: binding.traffic_support.percent }) }}</div>
                    <div v-if="bindingReason(binding)" class="mt-1 text-xs text-gray-500" :title="binding.error || (binding.status === 'preview' ? t('admin.sites.previewPaused') : '')">{{ binding.error || t(`admin.sites.status.${bindingReason(binding)}`) }}</div>
                  </td>
                  <td class="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-gray-100 pt-2 dark:border-dark-700 lg:table-cell lg:border-0 lg:px-5 lg:py-3 lg:text-right lg:whitespace-nowrap">
                    <button v-if="!bindingModel(binding)?.longxia && bindingModel(binding) && bindingModel(binding)?.vividai?.kind !== 'video'" class="min-h-11 text-primary-600 lg:mr-3 lg:min-h-0" :disabled="busy" @click="priceModel = bindingModel(binding)!">{{ t('admin.sites.manualPrice.edit') }}</button>
                    <button class="min-h-11 text-primary-600 lg:min-h-0" :disabled="busy" @click="openBinding(binding)">{{ t('admin.sites.details') }}</button>
                    <button class="min-h-11 text-red-600 lg:ml-3 lg:min-h-0" :disabled="busy" @click="confirmAction = { kind: 'binding', id: binding.id }">{{ t('admin.sites.unbind') }}</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <SiteModelCatalogue v-else-if="activeTab === 'models'" :key="selected.id" :site="selected" :busy="busy" :initial-only-new="onlyNewModels"
            @bind="model => openBinding(undefined, model)" @price="priceModel = $event" @read="modelsRead" />
          <div v-else class="space-y-3 p-4 sm:p-5">
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
                    .map((p) => `${p.key}: ${prices(p.prices, p.unit)}`)
                    .join(' / ')
                }}
                →
                {{
                  change.after
                    .map((p) => `${p.key}: ${prices(p.prices, p.unit)}`)
                    .join(' / ')
                }}
              </div>
            </div>
          </div>
        </section>
    </BaseDialog>
    <BaseDialog
      v-else-if="siteDialog"
      key="site-edit"
      :show="true"
      :title="t(editingId ? 'admin.sites.edit' : 'admin.sites.add')"
      @close="closeSite"
    >
      <form id="site-form" class="min-w-0 space-y-4 [overflow-wrap:anywhere]" @submit.prevent="saveSite">
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
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block min-w-0 text-sm"
            >{{ t('admin.sites.format')
            }}<select
              v-model="siteForm.kind"
              data-testid="site-kind"
              @change="changeSiteKind"
              class="input mt-1"
              :disabled="identityLocked"
            >
              <option value="vividai">VividAI</option>
              <option value="wuzu">WUZU（ChatGPT2API）</option>
              <option value="sub2api">Sub2API</option>
              <option value="newapi">New API</option>
              <option value="kongfang">{{ t('admin.sites.kongfang') }}</option>
            </select></label
          ><label class="block min-w-0 text-sm"
            >{{ t('admin.sites.auth')
            }}<select
              v-model="siteForm.auth_mode"
              class="input mt-1"
              :disabled="identityLocked"
            >
              <option v-if="siteForm.kind !== 'vividai'" value="password">
                {{ t('admin.sites.passwordLogin') }}
              </option>
              <option value="token">{{ siteForm.kind === 'vividai' ? 'API Key' : ['kongfang', 'wuzu'].includes(siteForm.kind) ? 'Access Token' : 'Access / Refresh Token' }}</option>
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
            >{{ siteForm.kind === 'vividai' ? 'API Key' : 'Access Token' }}<textarea
              v-model="siteForm.access_token"
              data-testid="site-access-token"
              class="input mt-1 font-mono text-xs"
              rows="3"
              autocomplete="off"
              :placeholder="editingId ? t('admin.sites.keepSecret') : ''"
            /></label
          ><label v-if="!['kongfang', 'vividai', 'wuzu'].includes(siteForm.kind)" class="block text-sm"
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
        <p class="text-xs text-gray-500">{{ t(siteForm.kind === 'wuzu' ? 'admin.sites.wuzuAuthHint' : siteForm.kind === 'vividai' ? 'admin.sites.vividaiAuthHint' : siteForm.kind === 'kongfang' ? 'admin.sites.kongfangAuthHint' : 'admin.sites.authHint') }}</p>
        <div class="space-y-2 text-sm">
          <label for="balance-conversion">{{ t('admin.siteBalance.conversion') }}</label>
          <div class="flex items-center gap-2">
            <span class="shrink-0">1 USD =</span>
            <input id="balance-conversion" v-model.number="siteForm.balance_units_per_usd" :placeholder="t('admin.siteBalance.autoConversion')" data-testid="balance-conversion" class="input min-w-0 flex-1" type="number" min="0.000000000001" max="1000000000000" step="any" />
            <span class="shrink-0">{{ conversionCurrency }}</span>
          </div>
          <div class="flex gap-2">
            <button v-for="rate in [100, 10, 1]" :key="rate" type="button" class="rounded border border-gray-200 px-3 py-1 text-xs dark:border-dark-600" @click="siteForm.balance_units_per_usd = rate">1:{{ rate }}</button>
          </div>
          <p class="text-xs text-gray-500">{{ t('admin.siteBalance.conversionHint') }}</p>
          <p v-if="siteForm.kind === 'wuzu'" class="text-xs text-gray-500">{{ t('admin.sites.wuzuConversionHint') }}</p>
          <p v-if="['kongfang', 'vividai'].includes(siteForm.kind)" class="text-xs text-gray-500">{{ t('admin.siteBalance.creditConversionHint') }}</p>
        </div>
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
      v-else-if="bindingDialog"
      key="site-binding"
      :show="true"
      :title="t(bindingForm.id ? 'admin.sites.details' : 'admin.sites.bind')"
      width="wide"
      @close="!busy && (bindingDialog = false)"
    >
      <form id="binding-form" class="min-w-0 space-y-4 [overflow-wrap:anywhere]" @submit.prevent="saveBinding">
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="block min-w-0 text-sm"
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
          ><label class="block min-w-0 text-sm"
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
          <label class="block min-w-0 text-sm"
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
          ><label class="block min-w-0 text-sm"
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
          <p v-if="bindingForm.price_tiers?.find((p) => p.key === limit.key)?.note" class="mt-2 text-xs text-gray-500">
            {{ bindingForm.price_tiers?.find((p) => p.key === limit.key)?.note }}
          </p>
          <div class="mt-3 grid gap-3 sm:grid-cols-3 text-xs">
            <div>
              <span class="text-gray-500">{{ t('admin.sites.price') }}</span>
              <p class="mt-1" data-testid="purchase-price">{{ prices(bindingForm.price_tiers?.find((p) => p.key === limit.key)?.prices || {}, limit.unit) }}</p>
            </div>
            <div>
              <span class="text-gray-500">{{ t('admin.sites.selling') }}</span>
              <p class="mt-1">{{ prices(limit.selling || {}, limit.unit) }}</p>
            </div>
            <div>
              <span class="text-gray-500">{{ t('admin.sites.limit') }}</span>
              <p class="mt-1 font-medium" data-testid="auto-limit">{{ prices(limit.limits, limit.unit) }}</p>
            </div>
          </div>
        </div>
        <label class="flex items-center gap-2 text-sm"
          ><input v-model="bindingForm.enabled" type="checkbox" />{{
            t('admin.sites.bindingEnabled')
          }}</label
        >
      </form>
        <SiteTrafficSupportForm v-if="bindingForm.id && bindingForm.account_id && selected" :key="bindingForm.id"
          :site-id="selected.id" :binding-id="bindingForm.id" :config="bindingForm.traffic_support"
          @saved="supportSaved" />

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
      v-else-if="confirmAction"
      key="site-confirm"
      :show="true"
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
    <SiteManualPriceDialog v-if="selected && priceModel" :key="JSON.stringify([selected.id, priceModel.group_id, priceModel.model])" :site-id="selected.id" :model="priceModel"
      @close="priceModel = null" @saved="manualPriceSaved" />
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import SiteBalanceCard from '@/components/admin/sites/SiteBalanceCard.vue'
import SitePriceSummary from '@/components/admin/sites/SitePriceSummary.vue'
import SiteOverview from '@/components/admin/sites/SiteOverview.vue'
import SiteTrafficSupportForm from '@/components/admin/sites/SiteTrafficSupportForm.vue'
import SiteManualPriceDialog from '@/components/admin/sites/SiteManualPriceDialog.vue'
import { sitePriceComponent } from '@/components/admin/sites/sitePriceFormat'
import SiteModelCatalogue from '@/components/admin/sites/SiteModelCatalogue.vue'
import { upstreamSiteBalanceApi, type SiteBalanceSettings } from '@/api/admin/upstreamSiteBalance'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { getAll, getModelAllowlistCandidates } from '@/api/admin/groups'
import {
  upstreamSitesApi,
  siteTierStatus,
  siteModelUsesCachedPrice,
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
const onlyNewModels = ref(false)
const now = ref(Date.now())
const detailsOpen = ref(Boolean(route.query.site))
const priceModel = ref<SiteModel | null>(null)
function manualPriceSaved(site: UpstreamSite) {
  replace(site)
  priceModel.value = null
}
const acknowledgedDiscoveries = new Map<string, Set<string>>()
let bindingDeepLinkOpened = false
watch(() => sites.value, () => {
  if (bindingDeepLinkOpened || !route.query.binding) return
  const binding = sites.value.find(s => s.id === selectedId.value)?.bindings.find(b => b.id === route.query.binding)
  if (binding) { bindingDeepLinkOpened = true; openBinding(binding) }
})
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
function selectSite(id: string) {
  detailsOpen.value = true
  selectedId.value = id
  activeTab.value = 'bindings'
  onlyNewModels.value = false
}
function openNewModels(id: string) {
  detailsOpen.value = true
  selectedId.value = id
  onlyNewModels.value = true
  activeTab.value = 'models'
}
function selectTab(tab: string) {
  onlyNewModels.value = false
  activeTab.value = tab
}
// Merge exact discovery IDs only. A concurrent sync may have found other models.
function modelsRead(siteId: string, ids: string[]) {
  const acknowledged = acknowledgedDiscoveries.get(siteId) || new Set<string>()
  for (const id of ids) acknowledged.add(id)
  acknowledgedDiscoveries.set(siteId, acknowledged)
  const site = sites.value.find(item => item.id === siteId)
  if (!site) return
  const read = new Set(ids)
  for (const model of site.models) if (model.discovery_id && read.has(model.discovery_id)) model.unread = false
}
function applyReadState(site: UpstreamSite) {
  const acknowledged = acknowledgedDiscoveries.get(site.id)
  for (const model of site.models) if (model.discovery_id && acknowledged?.has(model.discovery_id)) model.unread = false
  return site
}
const date = (value?: string) =>
  value ? new Date(value).toLocaleString() : '—'
const prices = (values: Record<string, number>, unit?: string) =>
  Object.entries(values)
    .map(
      ([k, v]) =>
        `${t(`admin.sites.components.${sitePriceComponent(k, unit)}`)} $${Number(v.toPrecision(8))}`,
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
      reason: bindingModel(b)?.reason || (!bindingModel(b) ? t('admin.sites.modelMissing') : undefined),
      manual_price: !!bindingModel(b)?.manual_price,
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
  applyReadState(site)
  const i = sites.value.findIndex((s) => s.id === site.id)
  if (i < 0) sites.value.push(site)
  else sites.value[i] = site
}
async function load() {
  now.value = Date.now()
  loading.value = true
  try {
    sites.value = (await upstreamSitesApi.list()).map(applyReadState)
    if (!sites.value.some((s) => s.id === selectedId.value)) {
      selectedId.value = ''
      detailsOpen.value = false
    }
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
  balance_units_per_usd: undefined,
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
const conversionCurrency = computed(() => sites.value.find(s => s.id === editingId.value)?.balance?.currency || (siteForm.value.kind === 'wuzu' ? t('admin.sites.wuzuUnits') : ['kongfang', 'vividai'].includes(siteForm.value.kind) ? '积分' : siteForm.value.kind === 'sub2api' ? 'USD' : t('admin.siteBalance.originalUnit')))
const identityLocked = computed(
  () => !!sites.value.find((s) => s.id === editingId.value)?.bindings.length,
)
function changeSiteKind() {
  if (siteForm.value.kind === 'wuzu') {
    siteForm.value.auth_mode = 'password'
    if (!siteForm.value.base_url) siteForm.value.base_url = 'https://img.wuzuapi.com'
  }
  siteForm.value.password = ''
  siteForm.value.access_token = ''
  siteForm.value.refresh_token = ''
  siteForm.value.username = ''
  siteForm.value.user_id = 0
  siteForm.value.usd_per_credit = 0
  siteForm.value.balance_units_per_usd = undefined
  if (siteForm.value.kind === 'vividai') {
    siteForm.value.auth_mode = 'token'
    if (!siteForm.value.base_url) siteForm.value.base_url = 'https://vividai.run'
  }
}
function editSite(site?: UpstreamSite) {
  editingId.value = site?.id || ''
  siteForm.value = site
    ? {
        ...emptySite(),
        usd_per_credit: site.usd_per_credit || 0,
        balance_units_per_usd: site.balance_units_per_usd || site.balance?.units_per_usd || (['kongfang', 'vividai'].includes(site.kind) && site.usd_per_credit ? 1 / site.usd_per_credit : site.kind === 'sub2api' ? 1 : undefined),
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
  const selectedWhenSaving = selectedId.value
  busy.value = true
  try {
    const result = await upstreamSitesApi.save(editingId.value, { ...siteForm.value, balance_units_per_usd: Number(siteForm.value.balance_units_per_usd) || 0, usd_per_credit: Number(siteForm.value.usd_per_credit) || 0, refresh_token: ['kongfang', 'vividai', 'wuzu'].includes(siteForm.value.kind) ? '' : siteForm.value.refresh_token })
    replace(result)
    if (selectedId.value === selectedWhenSaving) selectSite(result.id)
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
function supportSaved(config: import('@/api/admin/upstreamSites').SiteTrafficSupport) {
  bindingForm.value.traffic_support = config
  const binding = selected.value?.bindings.find(b => b.id === bindingForm.value.id)
  if (binding) binding.traffic_support = config
  app.showSuccess(t('admin.sites.support.saved'))
}
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
      ['openai', 'anthropic', 'gemini', 'grok'].includes(g.platform) &&
      (!['sub2api', 'kongfang', 'vividai', 'wuzu'].includes(selected.value?.kind || '') ||
        !chosenModel.value?.platform ||
        chosenModel.value.platform === g.platform ||
        (selected.value?.kind === 'sub2api' && chosenModel.value.platform === 'openai' && g.platform === 'grok' && chosenModel.value.model.toLowerCase().startsWith('grok-'))),
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
        traffic_support: undefined,
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
    if (!busy.value && !siteDialog.value && !bindingDialog.value && !priceModel.value) void load()
  }, 30000)
})
onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  siteForm.value = emptySite()
})
</script>
