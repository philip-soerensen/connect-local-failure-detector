// The bundle's entry: the app imports this file and calls what it default
// exports, handing it the one host object a module receives.
import { defineModule } from '@prusa/connect-ui/slots';

import { holdHost } from './host';
import SpaghettiTab from './spaghetti-tab.svelte';

export default defineModule((host) => {
  // The one contribution module.yaml declares; an id it does not carry is
  // refused and the module is dropped whole.
  holdHost(host);
  host.register.deviceTab('spaghetti', SpaghettiTab);
});
