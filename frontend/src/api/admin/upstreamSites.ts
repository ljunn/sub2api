import apiClient from '../client'

export interface SitePriceTier {
  key: string
  unit: string
  prices: Record<string, number>
  reason?: string
  note?: string
}
export interface SiteModel {
  group_id: string
  group_name: string
  model: string
  platform: string
  tiers: SitePriceTier[]
  reason?: string
}
export interface SiteTierLimit {
  key: string
  unit: string
  enabled: boolean
  limits: Record<string, number>
}
export interface SiteBinding {
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
  id: string
  name: string
  base_url: string
  kind: 'sub2api' | 'newapi'
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
  name: string
  base_url: string
  kind: 'sub2api' | 'newapi'
  auth_mode: 'password' | 'token'
  username: string
  user_id: number
  enabled: boolean
  password: string
  access_token: string
  refresh_token: string
}
export interface SiteAccountPolicy {
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
  if (!Number.isFinite(expiry) || expiry <= now) return 'expired'
  if (policy.reason || tier.reason || !Object.keys(tier.prices).length)
    return 'unknown'
  const limit = policy.limits.find((item) => item.key === tier.key)
  if (!limit || limit.unit !== tier.unit) return 'unknown'
  if (!limit.enabled) return 'disabled'
  for (const [key, value] of Object.entries(tier.prices)) {
    const cap = limit.limits[key]
    if (
      !Number.isFinite(cap) ||
      cap < 0 ||
      !Number.isFinite(value) ||
      value < 0
    )
      return 'unknown'
    if (value - cap > 1e-12 * Math.max(1, Math.abs(cap))) return 'exceeded'
  }
  return 'ready'
}
