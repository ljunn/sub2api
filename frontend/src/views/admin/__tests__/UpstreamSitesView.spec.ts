import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UpstreamSitesView from '../UpstreamSitesView.vue'
import type { UpstreamSite } from '@/api/admin/upstreamSites'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  sync: vi.fn(),
  bind: vi.fn(),
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
  it('binds a model alias with independent resolution ceilings and enablement', async () => {
    mocks.bind.mockResolvedValue(site)
    wrapper = mountView()
    await flushPromises()
    await click('admin.sites.bind')
    const selects = wrapper.findAll('#binding-form select')
    await selects[0]!.setValue('2')
    await selects[1]!.setValue('upstream-image')
    await selects[2]!.setValue('9')
    await wrapper.get('#binding-form input[list]').setValue('local-image')
    const caps = wrapper.findAll('#binding-form input[type="number"]')
    expect(caps).toHaveLength(3)
    await caps[1]!.setValue('0.15')
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
      [true, 0.1],
      [true, 0.15],
      [false, 0.3],
    ])
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
