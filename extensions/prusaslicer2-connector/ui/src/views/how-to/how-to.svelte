<!--
  The How to tab on this module's page under Settings, Extensions: where a
  person sets the connector up, which is on each printer and not here. A
  settings pane takes no props, and the host draws it with no heading, so
  the pane names itself.
-->
<script lang="ts">
  import { SettingsPane } from '@prusa/connect-ui/primitives';

  import { heldHost } from '../../host';

  const t = heldHost().t;
</script>

<SettingsPane title={t('howTo.title')} edged>
  <div
    class="grid max-w-3xl gap-section-gap p-panel-padding-mobile @lg/pane:p-panel-padding"
    data-part="how-to"
  >
    <p class="text-body">{t('howTo.perPrinter')}</p>
    <ol class="text-body grid list-decimal gap-2 ps-5 text-muted-foreground">
      <li>{t('howTo.steps.open')}</li>
      <li>{t('howTo.steps.copy')}</li>
      <li>{t('howTo.steps.slicer')}</li>
      <li>{t('howTo.steps.more')}</li>
    </ol>
    <p class="text-body text-muted-foreground">{t('howTo.keep')}</p>
    <p class="text-body text-muted-foreground">{t('howTo.limits')}</p>
  </div>
</SettingsPane>
