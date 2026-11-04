package api

import (
	"time"

	"github.com/wncservices/domestique/apps/api/internal/syncschedule"
)

// summaryNoonHour is the local hour from which a late pass no longer counts as
// the morning's: work gated on the day's first sync slot (the ledger, the
// reoptimiser, level decay, the LLM notes) must not start at 15:00.
const summaryNoonHour = 12

// inMorningSlot is morningSlotOf for the deployment's own schedule.
func (s *Server) inMorningSlot(now time.Time) bool {
	sched, err := s.syncSchedule()
	if err != nil {
		return false
	}
	return morningSlotOf(sched, now)
}

// beforeFirstSlot is whether no sync slot has yet passed on now's local date.
// A deployment whose schedule cannot be read is never "before": the hold it
// gates must not outlive a broken config.
func (s *Server) beforeFirstSlot(now time.Time) bool {
	sched, err := s.syncSchedule()
	if err != nil {
		return false
	}
	now = now.In(sched.Location())
	return sched.Last(now).In(sched.Location()).Format(dateLayout) != now.Format(dateLayout)
}

// morningSlotOf is the gate for work that belongs to the day's first sync slot:
// the latest slot at or before now is the day's first, on the same local date as
// now, and it is still before 12:00 local. A pass that runs late (the process
// was down at 06:30) still counts until noon.
func morningSlotOf(sched syncschedule.Schedule, now time.Time) bool {
	now = now.In(sched.Location())
	slot := sched.Last(now).In(sched.Location())
	if !sched.FirstOfDay(slot) || slot.Format(dateLayout) != now.Format(dateLayout) {
		return false
	}
	return now.Hour() < summaryNoonHour
}
