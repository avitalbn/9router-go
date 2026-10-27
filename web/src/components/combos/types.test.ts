import { describe, expect, it } from 'bun:test'
import {
  clearJudgeModel,
  COMBO_STRATEGIES,
  effectiveComboStrategy,
  MAX_FIRST_TOKEN_TIMEOUT_MS,
  resolveComboStrategy,
  updateComboStrategy,
  updateFirstTokenTimeout,
  type ComboStrategyInfo
} from './types'

const OPTION_VALUES = COMBO_STRATEGIES.map((s) => s.value)

describe('resolveComboStrategy', () => {
  // The server fills combo.strategy from settings.comboStrategies[name] and
  // otherwise from the global `comboStrategy`, so a card can receive either
  // vocabulary. Anything it can receive has to render as one of the options.
  it('maps every value the server can send onto an existing option', () => {
    const serverValues = [
      undefined,
      null,
      '',
      'fallback',
      'round-robin',
      'first-model',
      'sticky',
      'capacity',
      'fusion',
      'nonsense-from-a-hand-edited-backup'
    ]

    for (const value of serverValues) {
      expect(OPTION_VALUES).toContain(resolveComboStrategy(value))
    }
  })

  it('treats the global routing mode as the card fallback', () => {
    // `first-model` is the global Combo Routing Mode and the default when a
    // combo has no per-combo entry. It is try-in-order, which is exactly what
    // the card calls `fallback`.
    expect(resolveComboStrategy('first-model')).toBe('fallback')
  })

  it('passes a real per-combo strategy through unchanged', () => {
    for (const value of ['round-robin', 'sticky', 'capacity', 'fusion']) {
      expect(resolveComboStrategy(value)).toBe(value)
    }
  })

  it('falls back for a missing or unrecognised value', () => {
    expect(resolveComboStrategy(undefined)).toBe('fallback')
    expect(resolveComboStrategy(null)).toBe('fallback')
    expect(resolveComboStrategy('')).toBe('fallback')
    expect(resolveComboStrategy('nonsense')).toBe('fallback')
  })

  it('lists every option with a non-empty label', () => {
    for (const strategy of COMBO_STRATEGIES) {
      expect(strategy.value.length).toBeGreaterThan(0)
      expect(strategy.label.length).toBeGreaterThan(0)
    }
  })
})

describe('per-combo response timeout', () => {
  it('sets and disables only the timeout without mutating existing settings', () => {
    const current: Record<string, ComboStrategyInfo> = {
      primary: { fallbackStrategy: 'smart', judgeModel: 'judge', routerModel: 'router', sessionSticky: false },
      other: { firstTokenTimeoutMs: 750 }
    }
    const enabled = updateFirstTokenTimeout(current, 'primary', 2500)
    expect(enabled.primary).toEqual({ ...current.primary, firstTokenTimeoutMs: 2500 })
    expect(enabled.other).toBe(current.other)
    expect(current.primary.firstTokenTimeoutMs).toBeUndefined()

    const disabled = updateFirstTokenTimeout(enabled, 'primary', 0)
    expect(disabled.primary).toEqual({ ...current.primary, firstTokenTimeoutMs: 0 })
    expect(enabled.primary.firstTokenTimeoutMs).toBe(2500)
  })

  it('keeps inherited strategy when adding or disabling a timeout', () => {
    const enabled = updateFirstTokenTimeout({}, 'inherited', 1000)
    const disabled = updateFirstTokenTimeout(enabled, 'inherited', 0)
    for (const settings of [enabled, disabled]) {
      expect(settings.inherited.fallbackStrategy).toBeUndefined()
      expect(effectiveComboStrategy(settings.inherited.fallbackStrategy, 'round-robin')).toBe('round-robin')
    }
  })

  it('preserves timeout when switching to inherited strategy or clearing a judge', () => {
    const current = { primary: { fallbackStrategy: 'fusion', judgeModel: 'judge', firstTokenTimeoutMs: 1200 } }
    const inherited = updateComboStrategy(current, 'primary', 'inherit')
    expect(inherited.primary).toEqual({ judgeModel: 'judge', firstTokenTimeoutMs: 1200 })
    expect(clearJudgeModel(inherited, 'primary').primary).toEqual({ firstTokenTimeoutMs: 1200 })
  })

  it('accepts the backend maximum timeout', () => {
    expect(updateFirstTokenTimeout({}, 'primary', MAX_FIRST_TOKEN_TIMEOUT_MS)).toEqual({
      primary: { firstTokenTimeoutMs: MAX_FIRST_TOKEN_TIMEOUT_MS }
    })
  })

  it('rejects values outside the backend integer millisecond range', () => {
    const current = { primary: { firstTokenTimeoutMs: 1000 } }
    for (const invalid of [-1, 0.5, NaN, Infinity, MAX_FIRST_TOKEN_TIMEOUT_MS + 1, Number.MAX_SAFE_INTEGER + 1]) {
      expect(updateFirstTokenTimeout(current, 'primary', invalid)).toBe(current)
    }
  })
})
