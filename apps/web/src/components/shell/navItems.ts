// Plain data, no Vue: the nav is tested without a DOM and shared by the
// sidebar, the icon rail and the phone tab bar, so the three cannot drift.
export interface NavItem {
  group: 'you' | 'shared'
  label: string
  to: string
  icon: string
}

export const navItems: NavItem[] = [
  { group: 'you', label: 'Today', to: '/today', icon: 'i-lucide-calendar-check' },
  { group: 'you', label: 'Plan', to: '/plan', icon: 'i-lucide-calendar-range' },
  { group: 'you', label: 'Fitness', to: '/fitness', icon: 'i-lucide-heart-pulse' },
  { group: 'shared', label: 'Routes', to: '/routes', icon: 'i-lucide-route' },
  { group: 'shared', label: 'Crews', to: '/crews', icon: 'i-lucide-users-round' },
]

export const navGroupLabels: Record<NavItem['group'], string> = {
  you: 'You',
  shared: 'Shared',
}

export interface MenuItem {
  label: string
  to?: string
  /** Handled by the shell: a state change rather than a navigation. */
  action?: 'theme' | 'signOut'
  /** Set on the item that only informs; the shell renders it unclickable. */
  notice?: string
}

export interface MenuOptions {
  authMode?: 'none' | 'proxy' | 'oidc'
  /** How signing out works here: oidc ends the session with a POST, proxy
   *  hands the browser to the identity provider's own logout page. */
  signOut?: 'post' | 'link'
  logoutUrl?: string
}

export function menuItems(role: string, opts: MenuOptions = {}): MenuItem[] {
  const items: MenuItem[] = []
  // Under authMode none everyone is a local admin, which is worth saying
  // where the rider looks for who they are.
  if (opts.authMode === 'none') items.push({ label: 'Open access', notice: 'no login required' })
  items.push({ label: 'Settings', to: '/settings' })
  if (role === 'admin') items.push({ label: 'People', to: '/people' })
  items.push({ label: 'Theme', action: 'theme' })
  if (opts.signOut === 'post') items.push({ label: 'Sign out', action: 'signOut' })
  else if (opts.signOut === 'link' && opts.logoutUrl) items.push({ label: 'Sign out', to: opts.logoutUrl })
  return items
}
