import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import SitePriceSummary from '../SitePriceSummary.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('video site prices', () => {
  it.each([['USD/request', 'request'], ['USD/image', 'image']])('labels %s separately', (unit, component) => {
    const wrapper = mount(SitePriceSummary, { props: { tiers: [{ key: 'default', unit, prices: { request: .05 }, status: 'ready' }] } })
    expect(wrapper.get('[title]').attributes('title')).toContain(`components.${component} $0.05`)
    wrapper.unmount()
  })
  it('shows the second unit and includes configured selling and purchase limits', () => {
    const wrapper = mount(SitePriceSummary, { props: { tiers: [{
      key: '720p', unit: 'USD/second', prices: { second: .14 },
      selling: { second: .2 }, ceiling: { second: .2 }, status: 'ready',
    }] } })
    expect(wrapper.text()).toContain('$0.14/s')
    const details = wrapper.get('[title]').attributes('title')
    expect(details).toContain('USD/second')
    expect(details).toContain('$0.2')
    expect(details).not.toContain('components.request')
  })
})
