import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api, type Favorite } from '../api/client';
import { useFavorites } from './useFavorites';

const favorite: Favorite = { id: 'saved', path: 'folder', label: 'Folder', revision: 2, created_at: '', availability: 'available' };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return { client, ...renderHook(() => useFavorites(), { wrapper }) };
}

describe('useFavorites', () => {
  it('discards a late pre-mutation read so deleted favorites cannot return', async () => {
    let resolveOld!: (value: Awaited<ReturnType<typeof api.getFavorites>>) => void;
    const get = vi.spyOn(api, 'getFavorites').mockResolvedValueOnce({ ok: true, entries: [favorite] })
      .mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }))
      .mockResolvedValue({ ok: true, entries: [] });
    vi.spyOn(api, 'removeFavorite').mockResolvedValue({ ok: true, entry: favorite });
    const { client, result } = setup();
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true));
    void client.invalidateQueries({ queryKey: ['favorites'] });
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    await act(async () => { expect(await result.current.change({ kind: 'remove', entry: favorite })).toBe(true); });
    await act(async () => resolveOld({ ok: true, entries: [favorite] }));
    expect(client.getQueryData(['favorites'])).toEqual({ ok: true, entries: [] });
  });

  it('keeps the acknowledged revision if the post-write refresh fails', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValueOnce({ ok: true, entries: [favorite] }).mockRejectedValue(new Error('offline'));
    const updated = { ...favorite, label: 'Updated', revision: 3 };
    vi.spyOn(api, 'renameFavorite').mockResolvedValue({ ok: true, entry: updated });
    const { result, client } = setup();
    await waitFor(() => expect(result.current.query.isSuccess).toBe(true));
    await act(async () => { await result.current.change({ kind: 'rename', entry: favorite, label: 'Updated' }); });
    await waitFor(() => expect(result.current.query.isError).toBe(true));
    expect(client.getQueryData(['favorites'])).toEqual({ ok: true, entries: [updated] });
  });
});
