import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api, ApiError, type Favorite } from '../api/client';
import { I18nProvider } from '../i18n';
import { FavoritesPanel } from './FavoritesPanel';

const favorite: Favorite = { id: 'record', path: 'folder', label: 'Saved folder', revision: 1, created_at: '', availability: 'available' };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function mount(navigate = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider><FavoritesPanel directory="" onClose={() => {}} onNavigate={navigate} /></I18nProvider></QueryClientProvider>);
}

describe('FavoritesPanel', () => {
  it('does not navigate after the panel is closed during revalidation', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    let resolve!: (value: Awaited<ReturnType<typeof api.getDirectories>>) => void;
    vi.spyOn(api, 'getDirectories').mockImplementation(() => new Promise(done => { resolve = done; }));
    const navigate = vi.fn();
    const view = mount(navigate);
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }));
    view.unmount();
    await act(async () => { resolve({ ok: true } as Awaited<ReturnType<typeof api.getDirectories>>); });
    expect(navigate).not.toHaveBeenCalled();
  });

  it('keeps the current page when an existing favorite becomes unavailable', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    vi.spyOn(api, 'getDirectories').mockRejectedValue(new ApiError(404, 'not_found'));
    const navigate = vi.fn(); mount(navigate);
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }));
    await waitFor(() => expect(api.getDirectories).toHaveBeenCalledWith('folder'));
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(navigate).not.toHaveBeenCalled();
  });
  it('edits display metadata using the server revision', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    const rename = vi.spyOn(api, 'renameFavorite').mockResolvedValue({ ok: true, entry: { ...favorite, label: 'New label', revision: 2 } });
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Edit name' }));
    const inputs = screen.getAllByRole('textbox', { name: 'Display name' });
    fireEvent.change(inputs[1], { target: { value: 'New label' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    await waitFor(() => expect(rename).toHaveBeenCalledWith(favorite, 'New label'));
  });
});
