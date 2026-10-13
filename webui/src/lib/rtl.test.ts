import { describe, it, expect } from 'vitest'
import { detectDirection } from './rtl'

describe('detectDirection', () => {
  it('detects Arabic as rtl', () => {
    expect(detectDirection('كيف يبدو شكل حمار الوحش؟')).toBe('rtl')
  })

  it('detects Hebrew as rtl', () => {
    expect(detectDirection('מה נשמע?')).toBe('rtl')
  })

  it('detects English as ltr', () => {
    expect(detectDirection('What does a zebra look like?')).toBe('ltr')
  })

  it('skips markdown syntax before the first strong character', () => {
    expect(detectDirection('# عنوان رئيسي')).toBe('rtl')
    expect(detectDirection('- **الخطوط**: نمط مميز')).toBe('rtl')
    expect(detectDirection('> English quote')).toBe('ltr')
    expect(detectDirection('1. First item')).toBe('ltr')
  })

  it('lets the first strong character win in mixed text', () => {
    expect(detectDirection('Zebra (حمار وحشي) facts')).toBe('ltr')
    expect(detectDirection('حمار وحشي (Zebra) facts')).toBe('rtl')
  })

  it('falls back to ltr for neutral-only strings', () => {
    expect(detectDirection('')).toBe('ltr')
    expect(detectDirection('123 ... ???')).toBe('ltr')
  })
})
