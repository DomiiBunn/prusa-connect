<!--
  The fleet in five numbers: printing, needing attention, done, idle, and
  silent. Each printer is counted once. A printer that needs attention counts
  there, by the rule the issues card lists by (needsPerson in printers.ts),
  and every other printer counts by the tint of its state.

  It answers the question a wall is hung for, "is anything wrong", before any
  other: the count that needs attention is second only to what is printing,
  and it takes the fault tint the moment one printer is in error. The numbers
  grow with the tile (frontend-css.md, style math in CSS), so a summary
  across a whole wall reads from across the room.
-->
<script lang="ts">
  import { PRINTER_STATE_TONE, type PrinterStateTone } from '../../../domain';
  import { formatNumber } from '../../../i18n';
  import { StatusCard, type StatusTone, TilePanel } from '../../../primitives';

  import { needsPerson, selected } from '../printers';
  import { t } from '../../../i18n';
  import { dashboardContext } from '../context';
  import { tagSettings } from './config';
  import FleetState from './fleet-state.svelte';
  import type { CardProps } from '../../../slots';

  let { config, title }: CardProps = $props();

  const { printers } = dashboardContext();

  const settings = $derived(tagSettings(config));

  type Group = 'printing' | 'attention' | 'done' | 'idle' | 'silent';

  // Only for a printer that needs no attention. An error and a question on the
  // screen always need one, so the two loud tints never reach this map.
  const GROUP_OF: Readonly<Record<PrinterStateTone, Group>> = {
    active: 'printing',
    fault: 'attention',
    attention: 'attention',
    done: 'done',
    idle: 'idle',
    quiet: 'silent',
  };

  const GROUP_KEY: Readonly<Record<Group, string>> = {
    printing: 'dashboard.summary.printing',
    attention: 'dashboard.summary.attention',
    done: 'dashboard.summary.done',
    idle: 'dashboard.summary.idle',
    silent: 'dashboard.summary.silent',
  };

  const ORDER: readonly Group[] = [
    'printing',
    'attention',
    'done',
    'idle',
    'silent',
  ];

  const views = $derived(
    printers.views.filter((view) => selected(view, settings)),
  );

  const counts = $derived.by(() => {
    const held: Record<Group, number> = {
      printing: 0,
      attention: 0,
      done: 0,
      idle: 0,
      silent: 0,
    };
    for (const view of views) {
      const group = needsPerson(view)
        ? 'attention'
        : GROUP_OF[PRINTER_STATE_TONE[view.state]];
      held[group] += 1;
    }

    return held;
  });

  const faulted = $derived(views.some((view) => view.tier === 'critical'));

  /** The tone each count wears. A count of nothing wears none. */
  const toneOf = (group: Group): StatusTone => {
    if (counts[group] === 0) return 'quiet';
    if (group === 'attention') return faulted ? 'fault' : 'attention';
    if (group === 'printing') return 'active';
    if (group === 'silent') return 'quiet';

    return group;
  };

  const fallback = $derived(
    settings.tag === ''
      ? t('dashboard.summary.title', { count: views.length })
      : t('dashboard.summary.tagged', {
          count: views.length,
          tag: settings.tag,
        }),
  );
</script>

<TilePanel {title} {fallback}>
  <FleetState>
    <div data-part="fleet-counts" class="summary-counts min-h-0 flex-1">
      {#each ORDER as group (group)}
        <StatusCard
          edge
          title={t(GROUP_KEY[group])}
          tone={toneOf(group)}
          value={formatNumber(counts[group])}
          figure="fluid"
          headingLevel="h3"
        />
      {/each}
    </div>
  </FleetState>
</TilePanel>

<style>
  .summary-counts {
    display: grid;
    grid-template-columns: repeat(
      auto-fit,
      minmax(calc(var(--spacing) * 24), 1fr)
    );
    align-items: stretch;
    gap: calc(var(--spacing) * 3);
  }

  /* Where the grid gives the card a height, each count measures the room
     left under its heading, so a short card on a wall keeps its figures whole
     and inside the tile's padding. Measuring the whole grid instead sized the
     figure without the heading and the card's own padding, and on a two-row
     tile the cards ran past the bottom of the tile. A narrow page stacks the
     cards at the height of what they hold, and there nothing is measured. */
  @container board (width >= 40rem) {
    .summary-counts :global([data-slot='status-card']) {
      min-block-size: 0;
    }

    .summary-counts :global([data-slot='card-content']) {
      flex: 1;
      min-block-size: 0;
      container-type: size;
    }
  }
</style>
