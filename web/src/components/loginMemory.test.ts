import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { readLoginMemory, saveLoginMemory, clearLoginMemory } from './loginMemory';

beforeEach(() => localStorage.clear());
afterEach(() => { vi.restoreAllMocks(); document.querySelector('meta[name="fileharbor-base"]')?.remove(); });

it('isolates instances and preserves password whitespace', () => {
  expect(saveLoginMemory('alice', ' secret ')).toBe(true);
  const meta = document.createElement('meta'); meta.name = 'fileharbor-base'; meta.content = '/other/'; document.head.append(meta);
  expect(readLoginMemory().saved).toBeUndefined();
  saveLoginMemory('other', 'other-secret');
  clearLoginMemory(); meta.remove();
  expect(readLoginMemory().saved?.password).toBe(' secret ');
});

it('handles denied storage reads', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('Blocked'); });
  expect(readLoginMemory()).toEqual({ failed: true });
});

it.each(['null', '[]', '{"version":2,"username":"alice","password":"secret"}', '{"version":1,"username":"alice","password":3}'])('rejects invalid saved record %s', raw => {
  localStorage.setItem('fileharbor.login.v1:/', raw);
  expect(readLoginMemory()).toEqual({ failed: true });
});
