<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type FleetStatus } from '../api/client'

  let status = $state<FleetStatus | null>(null)
  let unavailable = $state(false)
  const safeAuthorityUrl = $derived(
    status?.authorityUrl?.startsWith('https://') ? status.authorityUrl : ''
  )
  onMount(() => {
    let disposed = false
    let timer: ReturnType<typeof setInterval> | undefined
    const refresh = async () => {
      try {
        const next = await api.getFleetStatus()
        if (!disposed) {
          status = next
          unavailable = false
          if (next.role && !timer) timer = setInterval(() => void refresh(), 60000)
        }
      } catch {
        if (!disposed) unavailable = true
      }
    }
    void refresh()
    return () => { disposed = true; if (timer) clearInterval(timer) }
  })
</script>

{#if status?.role}
  <aside class="mb-5 rounded-[10px] border border-border bg-surface-2 px-4 py-3 text-sm text-text-main">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
      <strong>{status.role === 'authority' ? 'Fleet authority' : 'Fleet follower'}</strong>
      <span class="text-text-muted">Revision {status.sync?.revision ?? 'pending'} · {status.runningCommit.slice(0, 8)}</span>
      {#if status.role === 'follower' && safeAuthorityUrl}
        <a class="text-primary underline" href={safeAuthorityUrl} target="_blank" rel="noopener noreferrer">Edit shared configuration on authority</a>
      {/if}
    </div>
    {#if status.role === 'follower'}
      <p class="mt-1 text-text-muted">Shared routing settings are read only here. Credentials and device settings stay local.</p>
    {/if}
    {#if status.sync?.lastError || status.sync?.codeError || unavailable}
      <p class="mt-1 text-text-muted" role="status">{status.sync?.lastError || status.sync?.codeError || 'Fleet status is temporarily unavailable.'}</p>
    {/if}
  </aside>
{/if}
