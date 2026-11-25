<!--
  The How to tab on this module's page under Settings, Extensions: where a
  person sets the connector up, which is once in PrusaSlicer and not on a
  printer. A settings pane takes no props, and the host draws it with no
  heading, so the pane names itself.
-->
<script lang="ts">
  import { CopyField, SettingsPane } from '@prusa/connect-ui/primitives';

  import { heldSession } from '../../session';

  let {
    origin = window.location.origin,
  }: {
    /** The server's address as the browser reached it. A test hands in its own. */
    origin?: string;
  } = $props();

  const t = heldSession().host.t;
</script>

<SettingsPane title={t('howTo.title')} edged>
  <div
    class="grid max-w-3xl gap-section-gap p-panel-padding-mobile @lg/pane:p-panel-padding"
    data-part="how-to"
  >
    <p class="text-body">{t('howTo.perServer')}</p>
    <ol class="text-body grid list-decimal gap-2 ps-5 text-muted-foreground">
      <li>
        {t('howTo.steps.services')}
        <CopyField class="mt-2" label={t('howTo.address')} value={origin} />
      </li>
      <li>{t('howTo.steps.signIn')}</li>
      <li>{t('howTo.steps.print')}</li>
      <li>{t('howTo.steps.more')}</li>
    </ol>
    <p class="text-body text-muted-foreground">{t('howTo.limits')}</p>
  </div>
</SettingsPane>
