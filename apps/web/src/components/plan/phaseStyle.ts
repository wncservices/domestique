// Colours for a periodization phase — shared by WeekStrip's header chip and
// SeasonTimeline's bars/bands/legend, so the four phases read as the same
// palette everywhere. They come from the phase scale (--color-phase-* in
// styles.css, see docs/design-system.md), which is deliberately a quiet
// neutral-to-teal ramp unlike the zone scale: a phase is not an effort.
import type { PeriodizationPhase } from '@/api/types'

export const phaseOrder: PeriodizationPhase[] = ['base', 'build', 'peak', 'taper']

export function phaseLabel(phase: PeriodizationPhase, recovery?: boolean): string {
  return phase.charAt(0).toUpperCase() + phase.slice(1) + (recovery ? ' · recovery' : '')
}

/** The phase's solid colour. */
export function phaseColor(phase: PeriodizationPhase): string {
  return `var(--color-phase-${phase})`
}

/** The phase colour softened over whatever is behind it. */
function phaseTint(phase: PeriodizationPhase, percent: number): string {
  return `color-mix(in oklab, ${phaseColor(phase)} ${percent}%, transparent)`
}

/** WeekStrip's header chip: a tinted pill with the normal text colour, since
 *  the lightest phases would not hold contrast as text. */
export function phaseChipStyle(phase: PeriodizationPhase): { class: string; style: Record<string, string> } {
  return { class: 'text-highlighted', style: { background: phaseTint(phase, 22) } }
}

/** SeasonTimeline's bar fill. */
export function phaseFill(phase: PeriodizationPhase): string {
  return phaseColor(phase)
}

/** SeasonTimeline's phase-band row: the same colour, softened, so the band
 *  reads as a background a bar sits on rather than competing with it. */
export function phaseBandFill(phase: PeriodizationPhase): string {
  return phaseTint(phase, 20)
}

/** SeasonTimeline's legend dot. */
export function phaseDotStyle(phase: PeriodizationPhase): { class: string; style: Record<string, string> } {
  return { class: '', style: { background: phaseColor(phase) } }
}
