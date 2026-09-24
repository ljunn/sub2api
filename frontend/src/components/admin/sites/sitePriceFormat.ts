export function sitePriceComponent(key: string, unit?: string): string {
  return key === 'request' && unit === 'USD/image' ? 'image' : key
}
