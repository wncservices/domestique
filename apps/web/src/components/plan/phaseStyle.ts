// Categorical colours for a periodization phase — shared by WeekStrip's own
// header chip and SeasonTimeline's bars/bands/legend, so the four phases
// read as the same palette everywhere rather than two independently-tuned
// colour sets. Base has no accent var (Nuxt UI's own neutral surface/text
// carries it instead) — see docs/design-system.md's categorical-vs-semantic
// rule for why a phase never borrows primary/success/warning/error.
import type { PeriodizationPhase } from '@/api/types'

const phaseAccent: Partial<Record<PeriodizationPhase, string>> = { build: 'sky', peak: 'ember', taper: 'violet' }

export const phaseOrder: PeriodizationPhase[] = ['base', 'build', 'peak', 'taper']

export function phaseLabel(phase: PeriodizationPhase, recovery?: boolean): string {
  return phase.charAt(0).toUpperCase() + phase.slice(1) + (recovery ? ' · recovery' : '')
}

/** WeekStrip's header chip: a soft-background/solid-text pill, or the
 *  neutral surface classes when there's no accent for this phase. */
export function phaseChipStyle(phase: PeriodizationPhase): { class: string; style: Record<string, string> } {
  const accent = phaseAccent[phase]
  if (!accent) return { class: 'bg-elevated text-muted', style: {} }
  return { class: '', style: { background: `var(--app-accent-${accent}-soft)`, color: `var(--app-accent-${accent})` } }
}

/** SeasonTimeline's bar fill — the phase's own solid accent, or
 *  'currentColor' for base so pairing it with `phaseFillClass` (below)
 *  resolves to the muted foreground instead of a made-up neutral colour. */
export function phaseFill(phase: PeriodizationPhase): string {
  const accent = phaseAccent[phase]
  return accent ? `var(--app-accent-${accent})` : 'currentColor'
}

export function phaseFillClass(phase: PeriodizationPhase): string {
  return phaseAccent[phase] ? '' : 'text-muted'
}

/** SeasonTimeline's phase-band row: the same accent, softened, so the band
 *  reads as a background a bar sits on rather than competing with it. */
export function phaseBandFill(phase: PeriodizationPhase): string {
  const accent = phaseAccent[phase]
  return accent ? `var(--app-accent-${accent}-soft)` : 'var(--ui-bg-elevated)'
}

/** SeasonTimeline's legend dot — an HTML element, so base needs
 *  `bg-current`/`text-muted` rather than an SVG `fill`. */
export function phaseDotStyle(phase: PeriodizationPhase): { class: string; style: Record<string, string> } {
  const accent = phaseAccent[phase]
  if (!accent) return { class: 'bg-current text-muted', style: {} }
  return { class: '', style: { background: `var(--app-accent-${accent})` } }
}
