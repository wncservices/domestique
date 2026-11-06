# Goals page

Goals get a page of their own. A rider can already hold several goals, but they sit in a collapsed
"Goals" toggle at the bottom of Plan, so it is hard to see them at a glance or add one. The page needs no backend change:
every endpoint it uses exists.

## What the page is

- **Route `/goals`**, lazy-loaded like every page (`router.ts`), and a sidebar entry next to Plan.
- **One card per goal** with:
  - name and priority;
  - a Focus badge, or "Set as focus";
  - the date or "rolling";
  - distance and climbing when set;
  - the linked route's name when there is one;
  - Edit and Delete.
- **Explain on each card.** It uses the existing `GET /api/training/goals/{id}/explain`, shows the
  answer inline on that card, and is hidden when `me.llmJobs.explainPlan` is off. A card shows "This can take up
  to a minute." while it waits, as the Plan header does.
- **New goal** opens the existing `GoalSlideover`. Its "describe your goal" sentence (`proposeGoal`)
  is the AI way to make one, and it saves nothing until the rider saves.
- **Empty state:** an invitation to add the first goal, with the same one-click options
  `PlanEmptyState` offers.

## What changes on Plan

- The header keeps the focus goal and its Explain button.
- The collapsed Goals toggle at the bottom becomes a link to `/goals`.
- `?goalFromRoute=<slug>` ("Train for this route") keeps working where it lands today.

## How

- **Reuse `usePlanGoals`** for loading, saving, deleting, focusing, proposing and explaining. Do not write a
  second copy of any of them.
- **Explanations are kept per goal** on the page, so explaining one card does not clear another's
  answer.
- **No new colours.** The page uses the theme tokens only.

## Tests

- The route table test lists `/goals`.
- A component test for the page shows:
  - one card per goal;
  - Explain hidden when the job is off;
  - an explanation shown on the card it was asked for.
