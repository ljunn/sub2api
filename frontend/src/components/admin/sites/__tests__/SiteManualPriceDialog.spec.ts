import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SiteManualPriceDialog from '../SiteManualPriceDialog.vue'
import type { SiteModel } from '@/api/admin/upstreamSites'
const saveModelPrice = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/upstreamSites', () => ({ upstreamSitesApi: { saveModelPrice } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const model: SiteModel = { group_id: '17', group_name: 'Images', model: 'gpt-image-2', platform: 'openai', image: true, reason: 'missing', tiers: [{ key: '1K', unit: 'USD/image', prices: { request: .008 }, reason: 'reference only' }] }
const mountDialog = (item = model) => mount(SiteManualPriceDialog, {
  props: { siteId: 'site', model: item },
  global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
})
beforeEach(() => { vi.clearAllMocks(); saveModelPrice.mockResolvedValue({ id: 'site' }) })
describe('manual purchase prices', () => {
  it('requires explicit prices and keeps blank resolutions distinct from free ones', async () => {
    const wrapper = mountDialog()
    expect((wrapper.get('[data-testid=manual-price-1K]').element as HTMLInputElement).value).toBe('')
    await wrapper.get('form').trigger('submit')
    expect(saveModelPrice).not.toHaveBeenCalled()
    expect(wrapper.get('[role=alert]').text()).toBe('admin.sites.manualPrice.required')
    await wrapper.get('[data-testid=manual-price-1K]').setValue('0')
    await wrapper.get('[data-testid=manual-price-2K]').setValue('0.012')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveModelPrice).toHaveBeenCalledWith('site', { group_id: '17', model: 'gpt-image-2', billing_mode: 'image', prices: { '1K': 0, '2K': .012 }, automatic: false })
    expect(wrapper.emitted('saved')).toEqual([[{ id: 'site' }]])
    wrapper.unmount()
  })
  it('edits saved values and can return to automatic prices', async () => {
    const wrapper = mountDialog({ ...model, manual_price: { group_id: '17', model: model.model, billing_mode: 'image', prices: { '1K': .02 } } })
    expect((wrapper.get('[data-testid=manual-price-1K]').element as HTMLInputElement).value).toBe('0.02')
    await wrapper.findAll('button').find(b => b.text() === 'admin.sites.manualPrice.auto')!.trigger('click')
    await flushPromises()
    expect(saveModelPrice.mock.calls[0]![1]).toMatchObject({ automatic: true, prices: {} })
    wrapper.unmount()
  })
  it('does not silently close when saving fails and discards fields from a different billing mode', async () => {
    saveModelPrice.mockRejectedValue({ response: { data: { message: '保存失败' } } })
    const wrapper = mountDialog()
    await wrapper.get('[data-testid=manual-price-1K]').setValue('0.01')
    await wrapper.get('[data-testid=manual-price-mode]').setValue('token')
    await wrapper.get('[data-testid=manual-price-input_price]').setValue('2')
    await wrapper.get('[data-testid=manual-price-output_price]').setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveModelPrice.mock.calls[0]![1].prices).toEqual({ input_price: 2, output_price: 5 })
    expect(wrapper.get('[role=alert]').text()).toBe('保存失败')
    expect(wrapper.emitted('saved')).toBeUndefined()
    wrapper.unmount()
  })
})
