import type { ProviderConnection, ProviderNode } from '../../api/client'
import { PROVIDER_ID_TO_ALIAS } from '../../lib/models'
import { PROVIDER_CATALOG } from '../../lib/providers'

/** Router model the gateway uses when a combo leaves `routerModel` empty. */
export const DEFAULT_ROUTER_MODEL = 'oc/jev-1.13-free'

/**
 * Providers the gateway answers for without a stored connection: every entry
 * of the Go registry (`providers.KnownProviders`) with `NoAuth` or a
 * `DefaultAPIKey`. smart_router.go querySystemOneRouter proceeds without a
 * connection only for these.
 *
 * Do not derive this from the catalog's `noAuth` flag: that flag drives the
 * dashboard's "no key needed" card and disagrees with the registry (it marks
 * devin-cli and selfhosted-* but not gemini-web, recraft or opencode-style
 * default keys). internal/providers/credential_free_test.go fails when the
 * registry and this list drift apart.
 */
// credential-free:start
export const REGISTRY_CREDENTIAL_FREE_PROVIDERS: ReadonlySet<string> = new Set([
  'comfyui',
  'coqui',
  'edge-tts',
  'gemini-web',
  'google-tts',
  'local-device',
  'mimo-free',
  'opencode',
  'recraft',
  'sdwebui',
  'searxng',
  'tortoise',
  'veoaifree-web',
])
// credential-free:end

let aliasToProvider: Map<string, string> | null = null

function aliasMap(): Map<string, string> {
  if (aliasToProvider) return aliasToProvider
  const map = new Map<string, string>()
  const add = (alias: string | undefined, id: string) => {
    if (alias && alias !== id && !map.has(alias)) map.set(alias, id)
  }
  for (const item of PROVIDER_CATALOG) add(item.alias, item.id)
  for (const [id, alias] of Object.entries(PROVIDER_ID_TO_ALIAS)) add(alias, id)
  aliasToProvider = map
  return map
}

/**
 * The provider id a `prefix/model` router string resolves to, following the
 * chat handler's order: a custom provider node's prefix first, then the
 * built-in alias table, then the prefix itself. Null when the string has no
 * `provider/` prefix (a bare model alias or combo name; the gateway resolves
 * those server-side and the dashboard cannot tell which provider they reach).
 */
export function resolveRouterProvider(
  routerModel: string,
  connections: ProviderConnection[] = [],
  providerNodes: ProviderNode[] = []
): string | null {
  const target = routerModel.trim() || DEFAULT_ROUTER_MODEL
  const slash = target.indexOf('/')
  if (slash <= 0) return null
  const prefix = target.slice(0, slash)

  const node = providerNodes.find((n) => n.prefix === prefix)
  if (node) return node.id
  const prefixed = connections.find((c) => (c.providerSpecificData?.prefix as string | undefined) === prefix)
  if (prefixed) return prefixed.provider
  return aliasMap().get(prefix) ?? prefix
}

/** An ACTIVE connection exists, or the registry serves this provider anonymously. */
export function providerHasCredentials(providerId: string, connections: ProviderConnection[] = []): boolean {
  if (REGISTRY_CREDENTIAL_FREE_PROVIDERS.has(providerId)) return true
  return connections.some((c) => c.isActive === 1 && c.provider === providerId)
}

/**
 * Mirrors smart_router.go querySystemOneRouter: the router model's provider
 * needs an active connection, or must be no-auth / carry a registry default key.
 * An empty `routerModel` is the default router model.
 */
export function routerHasCredentials(
  routerModel: string | undefined,
  connections: ProviderConnection[] = [],
  providerNodes: ProviderNode[] = []
): boolean {
  const provider = resolveRouterProvider(routerModel ?? '', connections, providerNodes)
  if (provider === null) return true
  return providerHasCredentials(provider, connections)
}

/** Warning for a combo card, or null when the router can authenticate. */
export function routerCredentialWarning(
  routerModel: string | undefined,
  connections: ProviderConnection[] = [],
  providerNodes: ProviderNode[] = []
): string | null {
  const provider = resolveRouterProvider(routerModel ?? '', connections, providerNodes)
  if (provider === null || providerHasCredentials(provider, connections)) return null
  return `No credentials for ${provider}: System One routing is skipped and the combo uses its saved order. Connect ${provider} or pick another router model.`
}
