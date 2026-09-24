import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import SiteOverview from '../SiteOverview.vue'
import SiteModelCatalogue from '../SiteModelCatalogue.vue'
import type { UpstreamSite } from '@/api/admin/upstreamSites'

const mocks = vi.hoisted(() => ({ markModelsRead: vi.fn() }))
vi.mock('@/api/admin/upstreamSites', async importOriginal => ({
  ...await importOriginal<typeof import('@/api/admin/upstreamSites')>(),
  upstreamSitesApi: mocks,
}))
vi.mock('@/api/client', () => ({ default: {} }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const makeSite = (id = 'site'): UpstreamSite => ({
  id, name: id, base_url: `https://${id}.example.com`, kind: 'sub2api', auth_mode: 'token', username: '', user_id: 1,
  enabled: true, status: 'connected', bindings: [], history: [],
  balance: { amount: 20, amount_usd: 20, currency: 'USD', last_success: new Date().toISOString() },
  models: ['one', 'two'].map(model => ({ group_id: 'g', group_name: 'Images', model, discovery_id: model, unread: true, platform: 'openai', tiers: [{ key: '1K', prices: { request: .01 }, unit: 'USD/image' }] })),
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.clearAllMocks() })

describe('site overview', () => {
  it('selects a site from the full row and keyboard without swallowing the new-model action', async () => {
    const wrapper = mount(SiteOverview, { props: { sites: [makeSite('first'), makeSite('second')], selectedId: 'first', threshold: 20, now: Date.now() } })
    const row = wrapper.get('[data-site=second]')
    for (const selector of ['td:first-child div', 'td:nth-child(4)', 'td:last-child']) {
      await row.get(selector).trigger('click')
    }
    await row.trigger('click')
    await row.trigger('keydown', { key: 'Enter' })
    await row.trigger('keydown', { key: ' ' })
    expect(wrapper.emitted('select')).toEqual(Array.from({ length: 6 }, () => ['second']))
    await row.get('button.bg-blue-50').trigger('click')
    expect(wrapper.emitted('models')).toEqual([['second']])
    expect(wrapper.emitted('select')).toHaveLength(6)
    await row.get('td:first-child button').trigger('click')
    expect(wrapper.emitted('select')).toHaveLength(7)
    const link = row.get('[data-testid=open-upstream-site]')
    expect(link.attributes('href')).toBe('https://second.example.com')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toContain('noopener')
    await link.trigger('click')
    expect(wrapper.emitted('select')).toHaveLength(7)
    wrapper.unmount()
  })
  it('shows low balances and unread models across sites and filters without reading anything', async () => {
    const low = makeSite('low'); low.balance!.amount_usd = 0
    const normal = makeSite('normal'); normal.models = []
    const stale = makeSite('stale'); stale.balance!.amount_usd = -1; stale.balance!.last_success = '2020-01-01'
    const disabled = makeSite('disabled'); disabled.enabled = false; disabled.balance!.amount_usd = -1
    const broken = makeSite('broken'); broken.status = 'error'; broken.balance = undefined
    const wrapper = mount(SiteOverview, { props: { sites: [low, normal, stale, disabled, broken], selectedId: 'normal', threshold: 20, now: Date.now() } })
    expect(wrapper.get('[data-filter=low]').text()).toContain('2')
    expect(wrapper.get('[data-filter=new]').text()).toContain('4')
    expect(wrapper.get('[data-filter=error]').text()).toContain('1')
    expect(wrapper.get('[data-site=low]').text()).toContain('0 USD')
    expect(wrapper.get('[data-site=stale]').text()).toContain('admin.sites.overview.balancePending')
    await wrapper.get('[data-filter=low]').trigger('click')
    expect(wrapper.findAll('[data-site]').map(row => row.attributes('data-site'))).toEqual(['low', 'stale'])
    await wrapper.get('input').setValue('stale')
    expect(wrapper.findAll('[data-site]')).toHaveLength(1)
    await wrapper.get('[data-site=stale] button.bg-blue-50').trigger('click')
    expect(wrapper.emitted('models')).toEqual([['stale']])
    expect(mocks.markModelsRead).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('model discovery read receipts', () => {
  it('allows configuring a binding for a model with unknown prices and explains reference prices', async () => {
    vi.stubGlobal('IntersectionObserver', class { observe = vi.fn(); disconnect = vi.fn() })
    const site = makeSite()
    site.models[0]!.tiers = []
    site.models[0]!.reason = '已读取模型，尚未确认价格，暂不参与价格调度'
    site.models[1]!.tiers[0]!.note = '分组图片参考价，尚未确认实际计费'
    const wrapper = mount(SiteModelCatalogue, { props: { site, busy: false, initialOnlyNew: false } })
    expect(wrapper.text()).toContain('分组图片参考价')
    expect(wrapper.text()).toContain(site.models[0]!.reason)
    await wrapper.get('[data-testid=catalogue-model] [data-testid=bind-model]').trigger('click')
    expect(wrapper.emitted('bind')).toEqual([[site.models[0]]])
    await wrapper.get('[data-testid=edit-model-price]').trigger('click')
    expect(wrapper.emitted('price')).toEqual([[site.models[0]]])
    wrapper.unmount()
  })
  function mountCatalogue() {
    let callback: IntersectionObserverCallback
    vi.stubGlobal('IntersectionObserver', class {
      constructor(cb: IntersectionObserverCallback) { callback = cb }
      observe = vi.fn(); unobserve = vi.fn(); disconnect = vi.fn()
    })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    mocks.markModelsRead.mockImplementation(async (_site, ids) => ({ discovery_ids: ids }))
    const wrapper = mount(SiteModelCatalogue, { props: { site: makeSite(), busy: false, initialOnlyNew: true } })
    const view = (indices: number[], ratio = 1) => callback(indices.map(index => ({
      target: wrapper.findAll('[data-testid=catalogue-model]')[index]!.element,
      isIntersecting: ratio > 0, intersectionRatio: ratio,
    } as IntersectionObserverEntry)), {} as IntersectionObserver)
    return { wrapper, view }
  }
  it('marks only visible models, keeps them on screen for binding, and excludes background tabs', async () => {
    const { wrapper, view } = mountCatalogue()
    expect(mocks.markModelsRead).not.toHaveBeenCalled()
    view([0], .2)
    await flushPromises()
    expect(mocks.markModelsRead).not.toHaveBeenCalled()
    view([0])
    await flushPromises()
    expect(mocks.markModelsRead).toHaveBeenCalledWith('site', ['one'])
    expect(wrapper.emitted('read')).toEqual([['site', ['one']]])
    expect(wrapper.findAll('[data-testid=catalogue-model]')).toHaveLength(2)
    expect(wrapper.emitted('bind')).toBeUndefined()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    view([1])
    await flushPromises()
    expect(mocks.markModelsRead).toHaveBeenCalledTimes(1)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    view([1])
    await flushPromises()
    expect(mocks.markModelsRead).toHaveBeenLastCalledWith('site', ['two'])
    await wrapper.findAll('[data-testid=catalogue-model] [data-testid=bind-model]')[0]!.trigger('click')
    expect(wrapper.emitted('bind')?.[0]?.[0]).toMatchObject({ model: 'one' })
    wrapper.unmount()
  })
  it('retains unread state after a failed save and allows retry', async () => {
    const { wrapper, view } = mountCatalogue()
    mocks.markModelsRead.mockRejectedValueOnce(new Error('offline'))
    view([0, 1])
    await flushPromises()
    expect(wrapper.emitted('read')).toBeUndefined()
    expect(wrapper.get('[role=alert]').text()).toContain('admin.sites.discovery.readFailed')
    await wrapper.get('[role=alert] button').trigger('click')
    await flushPromises()
    expect(wrapper.emitted('read')).toEqual([['site', ['one', 'two']]])
    expect(wrapper.find('[role=alert]').exists()).toBe(false)
    wrapper.unmount()
  })
})
