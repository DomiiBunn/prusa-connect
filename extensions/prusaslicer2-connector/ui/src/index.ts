// The bundle's entry. The app imports this file and calls what it default
// exports, handing it the one object a module receives.
//
// The module draws a card under the Connection card of each printer's
// Printer tab: the address and the API key a person types into PrusaSlicer 2
// for that printer. Its page under Settings, Extensions has a How to tab.
import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import { HowTo, PrusalinkRoute } from './views';
import './styles.css';

export default defineModule((host) => {
  holdHost(host);

  host.register.deviceInfoSection('prusalink', PrusalinkRoute);
  // Where the connector is set up, on its own page under Settings,
  // Extensions, since that page has no setting for one printer.
  host.register.settings('how-to', HowTo);
});
