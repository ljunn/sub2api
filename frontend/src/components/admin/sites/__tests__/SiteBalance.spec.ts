import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SiteBalanceCard from '../SiteBalanceCard.vue'
import SiteBalanceSettingsCard from '../SiteBalanceSettingsCard.vue'
import type { UpstreamSite } from '@/api/admin/upstreamSites'
import type { SiteBalanceSettings } from '@/api/admin/upstreamSiteBalance'

const api = vi.hoisted(() => ({ refresh: vi.fn(), settings: vi.fn(), saveSettings: vi.fn() }))
vi.mock('@/api/admin/upstreamSiteBalance', () => ({ upstreamSiteBalanceApi: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const settings: SiteBalanceSettings = { enabled: true, threshold: 20, admin_email: 'admin@example.com', smtp_configured: true }
const site = (amount?: number, error?: string): UpstreamSite => ({
  id: 'site', name: 'test', kind: 'sub2api', base_url: 'https://example.com', auth_mode: 'token', username: '', user_id: 1,
  enabled: true, status: 'connected', models: [], bindings: [], history: [],
  balance: { amount, currency: 'USD', last_success: new Date().toISOString(), error },
})
const wrappers: Array<{ unmount: () => void }> = []
beforeEach(() => vi.clearAllMocks())
afterEach(() => wrappers.splice(0).forEach((wrapper) => wrapper.unmount()))

describe('site balance', () => {
  it('shows zero as a real low balance and leaves missing balances unknown', () => {
    const zero = mount(SiteBalanceCard, { props: { site: site(0), settings } })
    const unknown = mount(SiteBalanceCard, { props: { site: site(), settings } })
    wrappers.push(zero, unknown)
    expect(zero.get('[data-testid="balance-amount"]').text()).toBe('0 USD')
    expect(zero.text()).toContain('admin.siteBalance.low')
    expect(unknown.text()).toContain('admin.siteBalance.stale')
    expect(unknown.text()).not.toContain('admin.siteBalance.low')
  })

  it('treats exactly 20 as sufficient and failed queries as stale', () => {
    const exact = mount(SiteBalanceCard, { props: { site: site(20), settings } })
    const failed = mount(SiteBalanceCard, { props: { site: site(10, 'upstream unavailable'), settings } })
    wrappers.push(exact, failed)
    expect(exact.text()).toContain('admin.siteBalance.normal')
    expect(failed.text()).toContain('10 USD')
    expect(failed.text()).toContain('admin.siteBalance.stale')
    expect(failed.get('[role="alert"]').text()).toBe('upstream unavailable')
  })

  it('refreshes the selected site and emits the persisted result', async () => {
    const result = site(8)
    api.refresh.mockResolvedValue(result)
    const wrapper = mount(SiteBalanceCard, { props: { site: site(21), settings } })
    wrappers.push(wrapper)
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(api.refresh).toHaveBeenCalledWith('site')
    expect(wrapper.emitted('updated')).toEqual([[result]])
    expect(wrapper.emitted('busy')).toEqual([[true], [false]])
  })

  it('loads and saves the recipient and threshold without hardcoded addresses', async () => {
    api.settings.mockResolvedValue({ ...settings })
    api.saveSettings.mockImplementation(async (value) => value)
    const wrapper = mount(SiteBalanceSettingsCard)
    wrappers.push(wrapper)
    await flushPromises()
    expect((wrapper.get('input[type="email"]').element as HTMLInputElement).value).toBe('admin@example.com')
    await wrapper.get('input[type="email"]').setValue('owner@example.com')
    await wrapper.get('input[type="number"]').setValue('15')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.saveSettings).toHaveBeenCalledWith({ ...settings, admin_email: 'owner@example.com', threshold: 15 })
    expect(wrapper.emitted('updated')?.at(-1)).toEqual([{ ...settings, admin_email: 'owner@example.com', threshold: 15 }])
  })

  it('cannot overwrite existing settings when loading fails', async () => {
    api.settings.mockRejectedValue(new Error('offline'))
    const wrapper = mount(SiteBalanceSettingsCard)
    wrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.get('button').attributes()).toHaveProperty('disabled')
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.siteBalance.settingsFailed')
    expect(api.saveSettings).not.toHaveBeenCalled()
  })
})
