// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
//
// The page is hidden from the sidebar. PrusaSlicer opens it by address, with
// `?embed=1`, so the app draws it without its frame (ADR-20261112-2eb rule
// 6).
//
// "Set as current" is registered only when PrusaSlicer shows the page, so a
// browser draws nothing for it (ADR-20261112-62y rule 5). PrusaSlicer is on
// the page before any script runs, so the answer does not change while the
// page is open.
import { slicer } from '@prusa/connect-ui/slicer';
import { defineModule } from '@prusa/connect-ui/slots';

import { apiOn } from './api';
import { holdSession } from './session';
import {
  HowTo,
  SelectPrinterRoute,
  SetAsCurrentHeader,
  SetAsCurrentRow,
} from './views';
import './styles.css';

export default defineModule((host) => {
  holdSession({ host, api: apiOn(host) });

  host.register.page('select-printer', SelectPrinterRoute);
  // Where the connector is set up, on its own page under Settings,
  // Extensions.
  host.register.settings('how-to', HowTo);

  if (slicer.available) {
    host.register.deviceHeader('set-as-current', SetAsCurrentHeader);
    host.register.deviceRow('set-as-current', SetAsCurrentRow);
  }
});
