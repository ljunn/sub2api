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
  it('edits video seconds without converting fixed request prices', async () => {
    const video = { ...model, model: 'grok-imagine-video', image: false, tiers: [] }
    const wrapper = mountDialog(video)
    expect((wrapper.get('[data-testid=manual-price-mode]').element as HTMLSelectElement).value).toBe('video')
    expect(wrapper.find('[data-testid=manual-price-1K]').exists()).toBe(false)
    await wrapper.get('[data-testid=manual-price-480p]').setValue('0')
    await wrapper.get('[data-testid=manual-price-720p]').setValue('0.02')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveModelPrice).toHaveBeenCalledWith('site', expect.objectContaining({ billing_mode: 'video', prices: { '480p': 0, '720p': .02 } }))
    wrapper.unmount()
    const fixed = mountDialog({ ...video, tiers: [{ key: 'default', unit: 'USD/request', prices: { request: .35 } }] })
    expect((fixed.get('[data-testid=manual-price-mode]').element as HTMLSelectElement).value).toBe('per_request')
    expect(fixed.text()).toContain('admin.sites.manualPrice.videoRequestPrice')
    expect(fixed.find('option[value=image]').exists()).toBe(false)
    fixed.unmount()
  })

  it.each(['grok-imagine-video', 'xai/grok-video-1.5'])('uses video prices for %s despite old image metadata', (name) => {
    const wrapper = mountDialog({ ...model, model: name })
    expect((wrapper.get('[data-testid=manual-price-mode]').element as HTMLSelectElement).value).toBe('video')
    expect(wrapper.find('option[value=image]').exists()).toBe(false)
    for (const key of ['1K', '2K', '4K']) expect(wrapper.find(`[data-testid=manual-price-${key}]`).exists()).toBe(false)
    for (const key of ['480p', '720p', '1080p']) {
      const input = wrapper.get(`[data-testid=manual-price-${key}]`)
      expect((input.element as HTMLInputElement).value).toBe('')
      expect(input.attributes('placeholder')).toBe('')
    }
    wrapper.unmount()
  })

  it('requires replacement video prices for a saved image override and supports restoring automatic prices', async () => {
    const video = { ...model, model: 'grok-imagine-video', tiers: [], manual_price: { group_id: '17', model: 'grok-imagine-video', billing_mode: 'image' as const, prices: { '1K': .02 } } }
    const wrapper = mountDialog(video)
    expect(wrapper.get('[role=status]').text()).toBe('admin.sites.manualPrice.videoImagePriceInvalid')
    expect(wrapper.find('option[value=image]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    expect(saveModelPrice).not.toHaveBeenCalled()
    await wrapper.get('[data-testid=manual-price-720p]').setValue('0.03')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveModelPrice).toHaveBeenCalledWith('site', expect.objectContaining({ billing_mode: 'video', prices: { '720p': .03 }, automatic: false }))
    wrapper.unmount()
    const automatic = mountDialog(video)
    await automatic.findAll('button').find(b => b.text() === 'admin.sites.manualPrice.auto')!.trigger('click')
    await flushPromises()
    expect(saveModelPrice).toHaveBeenLastCalledWith('site', expect.objectContaining({ automatic: true, prices: {} }))
    automatic.unmount()
  })

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
