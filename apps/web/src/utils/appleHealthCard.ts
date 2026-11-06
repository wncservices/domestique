import type { AppleHealthLink } from '@/api/types'

export type CardState = 'off' | 'just-linked' | 'linked'

/** The token exists only in the response that made it, so "just linked" means
 *  this page holds one in memory; after a reload the card is plain "linked". */
export function cardState(link: AppleHealthLink, justIssued: string | null): CardState {
  if (!link.linked) return 'off'
  return justIssued ? 'just-linked' : 'linked'
}

/** The deployment may host the shortcut itself; otherwise point at the guide. */
export function shortcutHelp(link: AppleHealthLink): { kind: 'link'; url: string } | { kind: 'docs' } {
  return link.shortcutUrl ? { kind: 'link', url: link.shortcutUrl } : { kind: 'docs' }
}
