import { describe, expect, it, vi } from 'vitest'
import { siteTierStatus, type SiteAccountPolicy } from '../admin/upstreamSites'

vi.mock('../client', () => ({ default: {} }))

describe('site tier equal-price display', () => {
  it('defaults to blocking equality, supports opt-in, and keeps higher prices blocked', () => {
    const policy: SiteAccountPolicy = {
      site_id: 'site', site_name: 'Site', binding_id: 'b', enabled: true,
      fresh_until: new Date(Date.now() + 60000).toISOString(),
      tiers: [{ key: '1K', unit: 'USD/image', prices: { request: .04 } }],
      limits: [{ key: '1K', unit: 'USD/image', enabled: true, limits: { request: .04 } }],
    }
    const tier = policy.tiers[0]!
    expect(siteTierStatus(policy, tier)).toBe('equal')
    policy.limits[0]!.allow_equal_price_scheduling = true
    expect(siteTierStatus(policy, tier)).toBe('ready')
    tier.prices.request = .041
    expect(siteTierStatus(policy, tier)).toBe('exceeded')
    policy.limits[0]!.allow_equal_price_scheduling = false
    tier.prices.request = .039
    tier.prices.free = 0
    policy.limits[0]!.limits.free = 0
    expect(siteTierStatus(policy, tier)).toBe('ready')
  })
})
