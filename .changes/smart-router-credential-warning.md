### feat(combos): warn when the Smart Router model has no credentials

A Smart Router combo whose `routerModel` points at a provider with no active
connection (for example `openrouter/typesafe/jev-1.13` with no OpenRouter
account) never routed: the gateway logged `smart-router: no credentials for
router provider` on every request and used the combo's saved order, and the
dashboard said nothing.

The combo card now shows an amber `No credentials` marker beside the router
chip, with a tooltip naming the provider and the consequence. The router
picker marks provider sections that have no active connection the same way.

The check mirrors `querySystemOneRouter`: credentials exist when the router
model's provider (alias prefixes and custom node prefixes resolved, empty
model = `oc/jev-1.13-free`) has an active connection, or is a registry
provider with `NoAuth` or a default key. It reuses the connection list the
view already loads, so there is no extra request and no polling. A Go test
fails if the registry's credential-free provider set and the dashboard copy
drift apart.
