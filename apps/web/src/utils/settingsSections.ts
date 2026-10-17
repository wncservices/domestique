import type { Role } from '@/api/types'

export interface SettingsSection {
  id: string
  label: string
  admin: boolean
}

// Role decides only which sections exist. What sits inside one still follows
// its own gate (a connection, a permission), so an admin on a deployment with
// nothing to configure gets a quiet section rather than a missing one.
export function settingsSections(role: Role): SettingsSection[] {
  const sections: SettingsSection[] = [
    { id: 'profile', label: 'Profile', admin: false },
    { id: 'devices', label: 'Devices and sync', admin: false },
    { id: 'training', label: 'Training', admin: false },
    { id: 'notifications', label: 'Notifications', admin: false },
  ]
  if (role === 'admin') {
    sections.push(
      { id: 'deployment', label: 'Deployment', admin: true },
      { id: 'basemap', label: 'Basemap', admin: true },
    )
  }
  return sections
}
