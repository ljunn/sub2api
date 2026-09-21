import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UpstreamSitesView from '../UpstreamSitesView.vue'
import SitePriceSummary from '@/components/admin/sites/SitePriceSummary.vue'
import SiteOverview from '@/components/admin/sites/SiteOverview.vue'
import SiteModelCatalogue from '@/components/admin/sites/SiteModelCatalogue.vue'
import type { UpstreamSite } from '@/api/admin/upstreamSites'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  sync: vi.fn(),
  bind: vi.fn(),
  pricePreview: vi.fn(),
  showError: vi.fn(),
}))
vi.mock('@/api/admin/upstreamSiteBalance', () => ({ upstreamSiteBalanceApi: { settings: async () => ({ enabled: true, threshold: 20, recipients: [], smtp_configured: true }) } }))
vi.mock('@/api/client', () => ({ default: {} }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/api/admin/upstreamSites', async () => ({
  ...(await vi.importActual<typeof import('@/api/admin/upstreamSites')>(
    '@/api/admin/upstreamSites',
  )),
  upstreamSitesApi: mocks,
}))
vi.mock('@/api/admin/groups', () => ({
  getAll: async () => [{ id: 9, name: 'Local', platform: 'openai' }],
  getModelAllowlistCandidates: async () => ['local-image'],
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn() }),
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
const mountView = () =>
  shallowMount(UpstreamSitesView, {
    global: {
      stubs: {
        SitePriceSummary,
        SiteOverview,
        AppLayout: { template: '<div><slot /></div>' },
        RouterLink: { template: '<a><slot /></a>' },
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
      },
    },
  })
let wrapper: ReturnType<typeof mountView>
let site: UpstreamSite
beforeEach(() => {
  vi.clearAllMocks()
  site = {
    id: 'site',
    name: 'Upstream',
    base_url: 'https://example.com',
    kind: 'sub2api',
    auth_mode: 'password',
    username: 'user@example.com',
    user_id: 1,
    enabled: true,
    status: 'connected',
    last_success: new Date().toISOString(),
    models: [
      {
        group_id: '2',
        group_name: 'Images',
        model: 'upstream-image',
        platform: 'openai',
        tiers: ['1K', '2K', '4K'].map((key, i) => ({
          key,
          unit: 'USD/image',
          prices: { request: (i + 1) / 10 },
        })),
      },
    ],
    bindings: [],
    history: [],
  }
  mocks.pricePreview.mockImplementation(async (_id, binding) => ({ ...binding, price_tiers: site.models[0]!.tiers, limits: site.models[0]!.tiers.map((tier, i) => ({ key: tier.key, unit: tier.unit, enabled: true, selling: { request: (i+1)*.25 }, limits: { request: (i+1)*.2 } })) }))
  mocks.list.mockResolvedValue([site])
  mocks.save.mockResolvedValue(site)
  mocks.sync.mockResolvedValue(site)
})
afterEach(() => wrapper?.unmount())
async function click(text: string) {
  await wrapper
    .findAll('button')
    .find((b) => b.text() === text)!
    .trigger('click')
  await flushPromises()
}

describe('upstream sites', () => {
  it('opens new models without binding and preserves read receipts across an older poll response', async () => {
    site.models[0]!.discovery_id = 'new-one'
    site.models[0]!.unread = true
    const staleResponse = JSON.parse(JSON.stringify(site))
    wrapper = mountView()
    await flushPromises()
    wrapper.findComponent(SiteOverview).vm.$emit('models', site.id)
    await flushPromises()
    const catalogue = wrapper.findComponent(SiteModelCatalogue)
    expect(catalogue.props('initialOnlyNew')).toBe(true)
    catalogue.vm.$emit('read', site.id, ['new-one'])
    await flushPromises()
    mocks.list.mockResolvedValue([staleResponse])
    await click('common.refresh')
    expect(wrapper.findComponent(SiteOverview).props('sites')[0].models[0].unread).toBe(false)
    expect(mocks.bind).not.toHaveBeenCalled()
  })
  it('shows per-tier price vetoes even when the managed account awaits release', async () => {
    site.models[0]!.tiers = ['1K', '2K', '4K'].map((key, i) => ({
      key, unit: 'USD/image', prices: { request: i === 0 ? 0.03 : 0.045 },
    }))
    site.bindings = [{
      id: 'binding', group_id: '2', model: 'upstream-image', local_group_id: 9,
      local_model: 'local-image', platform: 'openai', account_id: 25,
      enabled: true, status: 'preview',
      limits: site.models[0]!.tiers.map(tier => ({
        key: tier.key, unit: tier.unit, enabled: true,
        selling: { request: 0.04 }, limits: { request: 0.04 },
      })),
    }]
    wrapper = mountView()
    await flushPromises()
    const rows = wrapper.findAll('[data-testid="site-price-summary"] > span')
    expect(rows[0]!.text()).toContain('admin.sites.status.preview')
    expect(rows[1]!.text()).toContain('admin.sites.status.exceeded')
    expect(rows[2]!.text()).toContain('admin.sites.status.exceeded')
    expect(wrapper.text()).toContain('admin.sites.status.blocked')
    expect(wrapper.findAll('[data-testid="binding-row"]')).toHaveLength(1)
    expect(wrapper.findComponent({ name: 'SiteBalanceSettingsCard' }).exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.siteBalance.automaticHint')
    expect(wrapper.text()).not.toContain('admin.sites.status.ready')

    mocks.list.mockResolvedValue([{ ...site, last_success: new Date(Date.now() - 660000).toISOString() }])
    await click('common.refresh')
    expect(wrapper.findAll('[data-testid="site-price-summary"] > span').every(row => row.text().includes('admin.sites.status.expired'))).toBe(true)
  })

  it('saves Kongfang conversion and hides unsupported refresh tokens', async () => {
    site.kind = 'kongfang'
    site.usd_per_credit = 0.12
    site.auth_mode = 'token'
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('admin.sites.kongfang')
    await click('common.edit')
    expect(wrapper.get<HTMLInputElement>('[data-testid="usd-per-credit"]').element.value).toBe('0.12')
    expect(wrapper.get('#site-form').text()).not.toContain('Refresh Token')
    expect(wrapper.get<HTMLTextAreaElement>('#site-form textarea').element.value).toBe('')
    await wrapper.get('[data-testid="usd-per-credit"]').setValue('0.15')
    await wrapper.get('#site-form').trigger('submit')
    await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith('site', expect.objectContaining({ kind: 'kongfang', usd_per_credit: 0.15, refresh_token: '' }))
  })
  it('does not offer an OpenAI local group for a Kongfang Gemini model', async () => {
    site.kind = 'kongfang'
    site.models[0]!.platform = 'gemini'
    wrapper = mountView()
    await flushPromises()
    await click('admin.sites.bind')
    const selects = wrapper.findAll('#binding-form select')
    await selects[0]!.setValue('2')
    await selects[1]!.setValue('upstream-image')
    await flushPromises()
    expect(selects[2]!.find('option[value="9"]').exists()).toBe(false)
  })

  it('derives read-only prices from the local model and saves only tier enablement', async () => {
    mocks.bind.mockResolvedValue(site)
    wrapper = mountView()
    await flushPromises()
    await click('admin.sites.bind')
    const selects = wrapper.findAll('#binding-form select')
    await selects[0]!.setValue('2')
    await selects[1]!.setValue('upstream-image')
    await selects[2]!.setValue('9')
    await wrapper.get('#binding-form input[list]').setValue('local-image')
    await flushPromises()
    expect(wrapper.findAll('#binding-form input[type="number"]')).toHaveLength(0)
    expect(wrapper.findAll('[data-testid="auto-limit"]')).toHaveLength(3)
    expect(wrapper.get('#binding-form').text()).toContain('$0.4')
    expect(mocks.pricePreview).toHaveBeenLastCalledWith('site', expect.objectContaining({ local_group_id: 9, local_model: 'local-image' }))
    await wrapper
      .findAll('#binding-form input[type="checkbox"]')[2]!
      .setValue(false)
    await wrapper.get('#binding-form').trigger('submit')
    await flushPromises()
    const [id, binding] = mocks.bind.mock.calls[0]!
    expect(id).toBe('site')
    expect(binding).toMatchObject({
      group_id: '2',
      model: 'upstream-image',
      local_group_id: 9,
      local_model: 'local-image',
    })
    expect(
      binding.limits.map(
        (limit: { enabled: boolean; limits: { request: number } }) => [
          limit.enabled,
          limit.limits.request,
        ],
      ),
    ).toEqual([
      [true, undefined],
      [true, undefined],
      [false, undefined],
    ])
  })
  it('clears old ceilings and blocks saving when local pricing cannot be loaded', async () => {
    mocks.pricePreview.mockRejectedValue(new Error('offline'))
    wrapper = mountView()
    await flushPromises()
    await click('admin.sites.bind')
    const selects = wrapper.findAll('#binding-form select')
    await selects[0]!.setValue('2')
    await selects[1]!.setValue('upstream-image')
    await selects[2]!.setValue('9')
    await flushPromises()
    expect(wrapper.get('#binding-form').text()).toContain('admin.sites.pricingFailed')
    expect(wrapper.findAll('[data-testid="auto-limit"]')).toHaveLength(0)
    expect(wrapper.get('button[form="binding-form"]').attributes('disabled')).toBeDefined()
  })
  it('never repopulates saved login secrets when reopening the site dialog', async () => {
    wrapper = mountView()
    await flushPromises()
    await click('common.edit')
    const password = wrapper.get<HTMLInputElement>(
      '#site-form input[type="password"]',
    )
    expect(password.element.value).toBe('')
    await password.setValue('new-password')
    await wrapper.get('#site-form').trigger('submit')
    await flushPromises()
    expect(mocks.save.mock.calls[0]![1].password).toBe('new-password')
    await click('common.edit')
    expect(
      wrapper.get<HTMLInputElement>('#site-form input[type="password"]').element
        .value,
    ).toBe('')
  })
  it('keeps upstream sync failure visible on the selected site', async () => {
    mocks.sync.mockResolvedValue({
      ...site,
      status: 'error',
      error: '上游登录已失效',
    })
    wrapper = mountView()
    await flushPromises()
    await click('admin.sites.sync')
    expect(wrapper.get('[role="alert"]').text()).toBe('上游登录已失效')
    expect(mocks.showError).toHaveBeenCalledWith('上游登录已失效')
  })
})
