<script lang="ts">
  // A card that answers "what is this, and what is it doing". The device card,
  // the Fractal page and the Extensions page all draw this shape (earlier research
  // recommendation 6).
  import ArrowDownIcon from '@lucide/svelte/icons/arrow-down';
  import ArrowRightIcon from '@lucide/svelte/icons/arrow-right';
  import ArrowUpIcon from '@lucide/svelte/icons/arrow-up';

  import CardContent from '../card/card-content.svelte';
  import CardHeader from '../card/card-header.svelte';
  import Card from '../card/card.svelte';
  import type { StatusCardProps } from './types';
  import { dots, edges } from './variants';

  let {
    title,
    tone = 'idle',
    toneLabel,
    value,
    caption,
    trend,
    trendLabel,
    timestamp,
    icon,
    badge,
    children,
    action,
    edge = false,
    figure = 'display',
    headingLevel = 'h2',
    class: className,
  }: StatusCardProps = $props();
</script>

<Card
  data-slot="status-card"
  data-tone={tone}
  density={edge ? 'tight' : 'default'}
  class={[edge && ['border-s-4', edges[tone]], className]}
>
  <CardHeader>
    <div class="flex items-center gap-2">
      {#if icon}
        <span aria-hidden="true" class="flex shrink-0 items-center">
          {@render icon()}
        </span>
      {/if}
      <!-- The dot carries no meaning on its own. When it is the only sign of
           the state, `toneLabel` is the word a screen reader hears. -->
      {#if !edge}
        <span
          data-slot="status-dot"
          aria-hidden="true"
          class={['rounded-badge size-2 shrink-0', dots[tone]]}
        ></span>
      {/if}
      <svelte:element
        this={headingLevel}
        class={[
          'min-w-0',
          edge
            ? 'truncate text-xs text-muted-foreground'
            : 'text-lead font-semibold',
        ]}
      >
        {title}
      </svelte:element>
      {#if toneLabel}
        <span class="sr-only">{toneLabel}</span>
      {/if}
      {#if badge}
        <span data-slot="status-badge" class="mis-auto shrink-0">
          {@render badge()}
        </span>
      {/if}
    </div>
  </CardHeader>

  <CardContent class="flex flex-col gap-2">
    {#if value}
      <p
        data-slot="status-value"
        data-figure={figure}
        class={[
          'font-semibold tabular-nums',
          figure === 'display' && 'text-display',
          tone === 'quiet' && 'text-muted-foreground',
        ]}
      >
        {value}
      </p>
    {/if}
    {#if caption}
      <p class="text-caption text-muted-foreground">{caption}</p>
    {/if}
    {#if trendLabel !== undefined || timestamp !== undefined}
      <p
        data-slot="status-footnote"
        class="flex flex-wrap items-center gap-2 text-caption text-muted-foreground"
      >
        {#if trendLabel}
          <span
            data-slot="status-trend"
            data-variant={trend}
            class="flex items-center gap-1"
          >
            {#if trend === 'up'}
              <ArrowUpIcon aria-hidden="true" class="size-3 shrink-0" />
            {:else if trend === 'down'}
              <ArrowDownIcon aria-hidden="true" class="size-3 shrink-0" />
            {:else if trend === 'flat'}
              <ArrowRightIcon aria-hidden="true" class="size-3 shrink-0" />
            {/if}
            {trendLabel}
          </span>
        {/if}
        {#if timestamp}
          <span data-slot="status-timestamp">{timestamp}</span>
        {/if}
      </p>
    {/if}
    {@render children?.()}
    {#if action}
      <div class="mbs-1 flex items-center gap-2">
        {@render action()}
      </div>
    {/if}
  </CardContent>
</Card>

<style>
  /* The reading follows the room it is given. The caller makes the card's
     content a size container, so the figure measures the space under the
     heading and fills most of it with its 1.1 line height. */
  [data-figure='fluid'] {
    font-size: clamp(
      var(--text-2xl),
      min(40cqi, 90cqb),
      calc(var(--text-2xl) * 3)
    );
    line-height: 1.1;
  }
</style>
