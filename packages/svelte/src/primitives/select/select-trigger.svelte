<!--
  The box a person presses to open the list.

  The box, its border, its hover and its focus ring come from the shared
  control recipe, so a select and a facet popover standing in the same toolbar
  are one control twice rather than two kinds of control. Only the placeholder
  colour and the layout of the value are the select's own.

  bits-ui puts no `role` on the button, so it is set here to `combobox`, as
  Radix does. A plain button takes its name from its content, and WCAG 2.5.3
  then wants the shown value inside the `aria-label` a caller names it with. A
  combobox keeps the value apart from the name, which is what a select is.

  React draws the chevron through Radix's `Icon` part. bits-ui has no such
  part, so the glyph sits in the markup.

  A tone is written behind `data-[tone=…]:` and not as a bare utility. A
  utility with a variant is emitted after every bare one, so `bg-info` there
  beats the recipe's `bg-transparent` without tailwind-merge or `!important`.
-->
<script lang="ts">
  import ChevronDownIcon from '@lucide/svelte/icons/chevron-down';
  import { Select as SelectPrimitive } from 'bits-ui';

  import {
    CONTROL_SIZES,
    CONTROL_TRIGGER,
  } from '../control-trigger/control-trigger';
  import type { SelectTriggerProps, SelectTriggerTone } from './types';

  const TONES: Record<SelectTriggerTone, string> = {
    default: '',
    info: 'data-[tone=info]:border-info data-[tone=info]:bg-info data-[tone=info]:text-info-foreground data-[tone=info]:hover:bg-info',
  };

  const CHEVRON_TONES: Record<SelectTriggerTone, string> = {
    default: '',
    info: 'text-info-foreground',
  };

  let {
    ref = $bindable(null),
    class: className,
    size = 'md',
    tone = 'default',
    children,
    ...rest
  }: SelectTriggerProps = $props();
</script>

<SelectPrimitive.Trigger
  bind:ref
  role="combobox"
  data-slot="select-trigger"
  data-size={size}
  data-tone={tone}
  class={[
    CONTROL_TRIGGER,
    CONTROL_SIZES[size],
    TONES[tone],
    'data-[placeholder]:text-muted-foreground disabled:data-[placeholder]:text-disabled-foreground *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center *:data-[slot=select-value]:gap-2',
    className,
  ]}
  {...rest}
>
  {@render children?.()}
  <ChevronDownIcon class={['size-4', CHEVRON_TONES[tone]]} />
</SelectPrimitive.Trigger>
