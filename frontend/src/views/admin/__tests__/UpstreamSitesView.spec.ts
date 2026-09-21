import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UpstreamSitesView from '../UpstreamSitesView.vue'
import type { UpstreamSite } from '@/api/admin/upstreamSites'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  sync: vi.fn(),
  bind: vi.fn(),
  pricePreview: vi.fn(),
  showError: vi.fn(),
}))
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
        AppLayout: { template: '<div><slot /></div>' },
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
