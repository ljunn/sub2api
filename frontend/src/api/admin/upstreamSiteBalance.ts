import apiClient from '../client'
import type { UpstreamSite } from './upstreamSites'

export interface SiteBalance {
  amount?: number
  currency?: string
  last_attempt?: string
  last_success?: string
  next_check?: string
  error?: string
  last_notified?: string
  next_notify?: string
  notified_email?: string
  notify_error?: string
  low_since?: string
}

export interface SiteBalanceSettings {
  enabled: boolean
  threshold: number
  admin_email: string
  smtp_configured: boolean
}

const base = '/admin/upstream-sites'
export const upstreamSiteBalanceApi = {
  settings: async () =>
    (await apiClient.get<SiteBalanceSettings>(`${base}/balance-settings`)).data,
  saveSettings: async (input: SiteBalanceSettings) =>
    (await apiClient.put<SiteBalanceSettings>(`${base}/balance-settings`, input)).data,
  refresh: async (id: string) =>
    (await apiClient.post<UpstreamSite>(`${base}/${id}/balance`, {}, { timeout: 125000 })).data,
}
