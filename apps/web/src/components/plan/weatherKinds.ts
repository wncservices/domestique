// How each weather reason looks: one icon and one short word per kind, so a chip
// never relies on colour alone. The worst reason on a day picks the icon.
import type { WeatherWorst } from '@/api/types'

interface Kind {
  icon: string
  /** The short word on a chip. */
  label: string
  /** The heading of a banner ("Rain on Saturday"). */
  title: string
  color: 'warning' | 'error'
}

export const WEATHER_KINDS: Record<WeatherWorst, Kind> = {
  thunder: { icon: 'i-lucide-cloud-lightning', label: 'Storm', title: 'Thunderstorms', color: 'error' },
  wintry: { icon: 'i-lucide-snowflake', label: 'Snow or ice', title: 'Snow or ice', color: 'error' },
  rain: { icon: 'i-lucide-cloud-rain', label: 'Rain', title: 'Rain', color: 'warning' },
  wind: { icon: 'i-lucide-wind', label: 'Wind', title: 'Strong wind', color: 'warning' },
  cold: { icon: 'i-lucide-thermometer-snowflake', label: 'Cold', title: 'Cold', color: 'warning' },
  heat: { icon: 'i-lucide-thermometer-sun', label: 'Hot', title: 'Heat', color: 'warning' },
}

/** A kind for a day or suggestion whose worst reason is missing or unknown. */
export function weatherKind(worst: WeatherWorst | undefined): Kind {
  return (worst && WEATHER_KINDS[worst]) || WEATHER_KINDS.rain
}
