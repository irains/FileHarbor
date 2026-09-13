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
    await act(async () => { resolve({ ok: true, path: 'folder', dirs: [] }); });
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
    const input = screen.getByRole('textbox', { name: 'Display name' });
    fireEvent.change(input, { target: { value: 'New label' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    await waitFor(() => expect(rename).toHaveBeenCalledWith(favorite, 'New label'));
  });
  it('rejects fallback destinations and has no persistent naming form', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    vi.spyOn(api, 'getDirectories').mockResolvedValue({ ok: true, path: '', dirs: [] });
    const navigate = vi.fn(); mount(navigate);
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }));
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(navigate).not.toHaveBeenCalled();
  });

  it('navigates only after validating the saved path', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    vi.spyOn(api, 'getDirectories').mockResolvedValue({ ok: true, path: 'folder', dirs: [] });
    const navigate = vi.fn(); mount(navigate);
    fireEvent.click(await screen.findByRole('button', { name: 'Open' }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('folder'));
  });

  it('keeps inline editing and the error on a revision conflict', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    vi.spyOn(api, 'renameFavorite').mockRejectedValue(new ApiError(409, 'favorite_conflict'));
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Edit name' }));
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'Keep my draft' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.getByRole('textbox')).toHaveValue('Keep my draft');
    await waitFor(() => expect(api.getFavorites).toHaveBeenCalledTimes(2));
  });

  it('cancels editing with Escape and restores focus', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    mount();
    fireEvent.click(await screen.findByRole('button', { name: 'Edit name' }));
    fireEvent.keyDown(screen.getByRole('textbox'), { key: 'Escape' });
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Edit name' })).toHaveFocus());
  });

});
