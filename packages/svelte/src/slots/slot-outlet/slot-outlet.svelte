<script lang="ts">
  import { onMount } from 'svelte';

  import { t } from '../../i18n';
  import { Card, CardContent, CardHeader, CardTitle } from '../../primitives';
  import { getSlotContext } from '../context';
  import { matchesDevice } from '../device-selector';
  import { matchesFileRef } from '../file-types';
  import type { SectionSlot } from '../generated';
  import type { SlotOutletProps } from './types';

  // Kept whole rather than destructured: `deviceId`, `fileRef`, `camera` and `jobId` each sit on
  // only some members of the `SectionOutletTarget` union `SlotOutletProps`
  // extends, and reading them needs the 'in' narrowing below.
  const props: SlotOutletProps = $props();

  const context = getSlotContext();
  const registryInUse = $derived(props.registry ?? context?.registry);
  const loaderInUse = $derived(props.loader ?? context?.loader);

  // The layout of each section slot is a closed record: a new section slot is
  // a type error here until someone decides how it draws.
  type Layout =
    'pane' | 'section' | 'card' | 'line' | 'tab' | 'bare' | 'inline';
  const LAYOUT = {
    settings: 'pane',
    'device-card': 'line',
    'device-section': 'section',
    // A card with its title, as each of the core's own on the Printer tab
    // (ADR-20261124-e2e).
    'device-info-section': 'card',
    'device-tab': 'tab',
    'print-section': 'section',
    'file-view': 'section',
    'file-tab': 'tab',
    'camera-section': 'bare',
    'camera-view': 'bare',
    // No frame and no heading of the host's: the component is the whole
    // box, so a module that draws nothing leaves nothing on the screen
    // (ADR-20261112-62y).
    'device-header': 'inline',
    'device-row': 'inline',
    // A table cell is the frame, and the column's heading is the title the
    // manifest names, so the value is drawn alone.
    'job-column': 'inline',
    'job-section': 'card',
  } as const satisfies Record<SectionSlot, Layout>;

  // An entry that names file types is drawn only for a file of one of them
  // (ADR-20261027-wvj rule 17). An entry with a devices selector is drawn
  // only for a device that meets it (ADR-20261110-5np).
  const rows = $derived(
    (registryInUse?.sections(props.name) ?? []).filter(
      (row) =>
        (props.entry === undefined ||
          `${row.namespace}.${row.id}` === props.entry) &&
        (props.namespace === undefined || row.namespace === props.namespace) &&
        (!('fileRef' in props) ||
          matchesFileRef(row.fileTypes, props.fileRef)) &&
        (!('deviceId' in props) || matchesDevice(row.devices, props.device)),
    ),
  );
  const subject = $derived.by(() => {
    if ('deviceId' in props) return { deviceId: props.deviceId };
    if ('fileRef' in props) return { fileRef: props.fileRef };
    if ('camera' in props) return { camera: props.camera };
    if ('jobId' in props) return { jobId: props.jobId };

    return {};
  });

  // Activation is lazy: the bundle is imported when its first slot first
  // renders (ADR-20260911-4p6 rule 25). The loader runs once however many outlets
  // ask, so every outlet on the screen may call this.
  onMount(() => {
    void loaderInUse?.activate();
  });

  const dropped = (namespace: string, error: unknown) => {
    registryInUse?.refuse({
      namespace,
      reason: 'threw',
      detail: error instanceof Error ? error.message : String(error),
    });
  };
</script>

<!-- One boundary per registration and not one around the list: a section that
     throws is dropped on its own and the others stay (ADR-20260911-4p6 rule 26). -->
{#each rows as row (row.namespace + '/' + row.id)}
  {@const Row = row.component}
  <svelte:boundary
    onerror={(error) => {
      dropped(row.namespace, error);
    }}
  >
    {#if LAYOUT[props.name] === 'section'}
      <section
        class={['flex flex-col gap-2', props.class]}
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
      >
        <h3 class="text-sm font-semibold">{row.title}</h3>
        <Row {...subject} />
      </section>
    {:else if LAYOUT[props.name] === 'card'}
      <!-- A card whose component drew nothing, such as a module section about
           a print the module knows nothing of, is hidden with its title. -->
      <div
        class={['module-card', props.class]}
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
      >
        <Card>
          <CardHeader>
            <CardTitle><h2 class="contents">{row.title}</h2></CardTitle>
          </CardHeader>
          <CardContent>
            <Row {...subject} />
          </CardContent>
        </Card>
      </div>
    {:else if LAYOUT[props.name] === 'bare'}
      <!-- Drawn under every camera, and most modules feed only some of them,
           so there is no heading: the module draws its own, and a section
           that draws nothing takes no room (ADR-20261106-xm4 rule 5). -->
      <div
        class={['flex flex-col gap-2 empty:hidden', props.class]}
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
      >
        <Row {...subject} />
      </div>
    {:else if LAYOUT[props.name] === 'pane'}
      <!-- The tab label is the settings screen's to draw, so a pane renders
           without a heading of its own. -->
      <section
        class={['flex flex-col gap-2', props.class]}
        data-slot={props.name}
        data-module={row.namespace}
        data-pane={row.id}
      >
        <Row {...subject} />
      </section>
    {:else if LAYOUT[props.name] === 'tab'}
      <!-- The tab's label is drawn by the tab strip, so the panel is the
           component alone. -->
      <div
        class={['flex flex-col gap-2', props.class]}
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
      >
        <Row {...subject} />
      </div>
    {:else if LAYOUT[props.name] === 'inline'}
      <!-- `contents` draws no box, so the component's own root is the item
           of whatever layout the screen puts the outlet in. -->
      <div
        class="contents"
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
      >
        <Row {...subject} />
      </div>
    {:else}
      <!-- One line, cut rather than wrapped or grown: several registrations
           would otherwise push the card's height around as each loads. -->
      <div
        class={[
          'h-lh overflow-hidden text-sm text-muted-foreground [&>*]:truncate',
          props.class,
        ]}
        data-slot={props.name}
        data-module={row.namespace}
        data-section={row.id}
        role="group"
        aria-label={row.title}
      >
        <Row {...subject} />
      </div>
    {/if}
    {#snippet failed()}
      {#if LAYOUT[props.name] === 'tab'}
        <!-- Matches module-page's own failed panel, so a tab that throws
             reads the same as a page that throws. Every other layout draws
             nothing on failure, as it did before this snippet existed. -->
        <p class="text-sm text-muted-foreground">{t('modulePage.failed')}</p>
      {/if}
    {/snippet}
  </svelte:boundary>
{/each}

{#if LAYOUT[props.name] === 'tab' && rows.length === 0 && loaderInUse?.ready}
  <!-- Nothing registered the entry the URL names: the manifest declares it
       but the bundle has not, or never will. -->
  <p class="text-sm text-muted-foreground">{t('modulePage.failed')}</p>
{/if}

<style>
  /* A card whose component drew nothing hides with its title. The content is
     the Card's own element, so the selector reaches it through :global. */
  .module-card:has(:global([data-slot='card-content']:empty)) {
    display: none;
  }
</style>
