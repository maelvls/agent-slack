import { EnvHttpProxyAgent, setGlobalDispatcher } from "undici";

let globalDispatcherInstalled = false;

/**
 * Route the runtime's global `fetch` (WebClient/canvas/upload/download/update-check calls)
 * through HTTPS_PROXY/HTTP_PROXY/NO_PROXY when set. Node's built-in fetch doesn't
 * read these env vars itself; undici's global dispatcher is shared with it via a
 * well-known Symbol, so setting it here also affects `fetch` calls elsewhere in
 * the app. Safe to call multiple times or under Bun, whose fetch already honors
 * these env vars natively.
 */
export function installProxyDispatcher(): void {
  if (globalDispatcherInstalled) {
    return;
  }
  globalDispatcherInstalled = true;
  if (process.versions.bun) {
    // Bun's fetch already reads HTTPS_PROXY/HTTP_PROXY/NO_PROXY natively; overriding
    // undici's global dispatcher here has no effect on it and risks masking that behavior.
    return;
  }
  setGlobalDispatcher(new EnvHttpProxyAgent());
}
