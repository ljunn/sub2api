import apiClient from '../client'
import type { SiteBalance } from './upstreamSiteBalance'

export interface SitePriceTier {
  key: string
  unit: string
  prices: Record<string, number>
  reason?: string
  note?: string
}
export interface SiteTrafficSupport {
  enabled: boolean
  percent: number
  expires_at?: string
}
export interface SiteTrafficStatus {
  target: number
  effective: number
  actual: number
  first_attempts: number
  total: number
  state: string
}
export interface SiteSchedulingTier extends SitePriceTier {
  traffic_support?: SiteTrafficStatus
  selling?: Record<string, number>
  ceiling?: Record<string, number>
  status: string
  priority_score?: SitePriorityScore
}
export interface SitePriorityScore {
  score: number
  priority: number
  samples: number
  successes: number
  success_rate: number
  p50_seconds: number
  p90_seconds: number
  speed_seconds: number
  speed_reference: number
  speed_estimated: boolean
  cost_ratio: number
  ready: boolean
}
export interface SiteAccountScheduling {
  site_id?: string
  binding_id?: string
  status: 'ready' | 'partial' | 'blocked'
  reason?: string
  checked_at: string
  tiers: SiteSchedulingTier[]
}
export interface SiteManualPrice {
  group_id: string
  model: string
  billing_mode: 'image' | 'per_request' | 'token'
  prices: Record<string, number>
}
export interface SiteModel {
  longxia?: { resolution: string; duration_min: number; duration_max: number }
  vividai?: { kind: string; qualities: string[] }
  image?: boolean
  manual_price?: SiteManualPrice
 discovery_id?: string
 discovered_at?: string
 unread?: boolean
  group_id: string
  group_name: string
  model: string
  platform: string
  tiers: SitePriceTier[]
  reason?: string
}
export interface SiteTierLimit {
  allow_equal_price_scheduling?: boolean
  selling?: Record<string, number>
  reason?: string
  key: string
  unit: string
  enabled: boolean
  limits: Record<string, number>
}
export interface SiteBinding {
  traffic_support?: SiteTrafficSupport
  price_tiers?: SitePriceTier[]
  id: string
  group_id: string
  model: string
  local_group_id: number
  local_model: string
  platform: string
  account_id: number
  enabled: boolean
  limits: SiteTierLimit[]
  status: string
  error?: string
}
export interface SitePriceChange {
  at: string
  group_id: string
  model: string
  before: SitePriceTier[]
  after: SitePriceTier[]
}
export interface UpstreamSite {
  warnings?: string[]
  balance_units_per_usd?: number
  usd_per_credit?: number
  balance?: SiteBalance
  id: string
  name: string
  base_url: string
  kind: 'sub2api' | 'newapi' | 'kongfang' | 'vividai' | 'wuzu'
  auth_mode: 'password' | 'token'
  username: string
  user_id: number
  enabled: boolean
  status: string
  error?: string
  last_attempt?: string
  last_success?: string
  next_sync?: string
  models: SiteModel[]
  bindings: SiteBinding[]
  history: SitePriceChange[]
}
export interface SiteInput {
  balance_units_per_usd?: number
  usd_per_credit?: number
  name: string
  base_url: string
  kind: 'sub2api' | 'newapi' | 'kongfang' | 'vividai' | 'wuzu'
  auth_mode: 'password' | 'token'
  username: string
  user_id: number
  enabled: boolean
  password: string
  access_token: string
  refresh_token: string
}
export interface SiteAccountPolicy {
  manual_price?: boolean
  local_group_id?: number
  site_id: string
  site_name: string
  binding_id: string
  enabled: boolean
  fresh_until: string
  tiers: SitePriceTier[]
  limits: SiteTierLimit[]
  reason?: string
}
const base = '/admin/upstream-sites'
export const upstreamSitesApi = {
  saveTrafficSupport: async (id: string, bindingId: string, config: SiteTrafficSupport) =>
    (await apiClient.put<SiteTrafficSupport>(`${base}/${id}/bindings/${bindingId}/traffic-support`, config)).data,
  saveModelPrice: async (id: string, input: SiteManualPrice & { automatic?: boolean }) =>
    (await apiClient.put<UpstreamSite>(`${base}/${id}/model-price`, input)).data,
  markModelsRead: async (id: string, discovery_ids: string[]) =>
  (await apiClient.post<{ discovery_ids: string[] }>(`${base}/${id}/models/read`, { discovery_ids })).data,
  list: async () => (await apiClient.get<UpstreamSite[]>(base)).data,
  save: async (id: string, input: SiteInput) =>
    (
      await (id
        ? apiClient.put<UpstreamSite>(`${base}/${id}`, input)
        : apiClient.post<UpstreamSite>(base, input))
    ).data,
  sync: async (id: string) =>
    (
      await apiClient.post<UpstreamSite>(
        `${base}/${id}/sync`,
        {},
        { timeout: 125000 },
      )
    ).data,
  pricePreview: async (id: string, binding: SiteBinding) =>
    (await apiClient.post<SiteBinding>(`${base}/${id}/price-preview`, binding)).data,
  bind: async (id: string, binding: SiteBinding) =>
    (
      await (binding.id
        ? apiClient.put<UpstreamSite>(
            `${base}/${id}/bindings/${binding.id}`,
            binding,
            { timeout: 125000 },
          )
        : apiClient.post<UpstreamSite>(`${base}/${id}/bindings`, binding, {
            timeout: 125000,
          }))
    ).data,
  unbind: async (id: string, bindingId: string) =>
    (
      await apiClient.delete<UpstreamSite>(
        `${base}/${id}/bindings/${bindingId}`,
      )
    ).data,
  remove: async (id: string) => {
    await apiClient.delete(`${base}/${id}`)
  },
}

export function siteTierStatus(
  policy: SiteAccountPolicy,
  tier: SitePriceTier,
  now = Date.now(),
): string {
  if (!policy.enabled) return 'disabled'
  const expiry = Date.parse(policy.fresh_until)
  if (!policy.manual_price && (!Number.isFinite(expiry) || expiry <= now)) return 'expired'
  if (policy.reason || tier.reason || !Object.keys(tier.prices).length)
    return 'unknown'
  const limit = policy.limits.find((item) => item.key === tier.key)
  if (!limit || limit.unit !== tier.unit || limit.reason) return 'unknown'
  if (!limit.enabled) return 'disabled'
  let hasPositivePrice = false
  let equalPrice = false
  for (const [key, value] of Object.entries(tier.prices)) {
    const cap = limit.limits[key]
    if (
      !Number.isFinite(cap) ||
      cap < 0 ||
      !Number.isFinite(value) ||
      value < 0
    )
      return 'unknown'
    const tolerance = 1e-12 * Math.max(1, Math.abs(cap))
    if (value - cap > tolerance) return 'exceeded'
    if (value > 0 || cap > 0) {
      hasPositivePrice = true
      equalPrice ||= Math.abs(value - cap) <= tolerance
    }
  }
  if (!limit.allow_equal_price_scheduling && (equalPrice || !hasPositivePrice)) return 'equal'
  return 'ready'
}
