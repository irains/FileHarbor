import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api, ApiError, type Favorite } from '../api/client';
import { I18nProvider } from '../i18n';
import { FavoriteButton } from './FavoriteButton';
import { FavoritesPanel } from './FavoritesPanel';

const favorite: Favorite = { id: 'record', path: 'folder', label: 'Saved folder', revision: 7, created_at: '', availability: 'available' };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function mount(directory = 'folder', selected = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = (path: string) => <QueryClientProvider client={client}><I18nProvider><FavoriteButton directory={path} selected={selected} /></I18nProvider></QueryClientProvider>;
  const view = render(tree(directory));
  return { ...view, client, setDirectory: (path: string) => view.rerender(tree(path)) };
}
const toggle = () => screen.getAllByRole('button').find(button => button.hasAttribute('aria-pressed'))!;

describe('FavoriteButton', () => {
  it('adds the current root with its default name without file mutation permissions', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [] });
    const add = vi.spyOn(api, 'addFavorite').mockResolvedValue({ ok: true, entry: { ...favorite, path: '', label: 'Root' } });
    mount('');
    await waitFor(() => expect(toggle()).toBeEnabled());
    fireEvent.click(toggle());
    await waitFor(() => expect(add).toHaveBeenCalledWith('', 'Root'));
  });

  it('removes a selected directory using its server revision', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    const remove = vi.spyOn(api, 'removeFavorite').mockResolvedValue({ ok: true, entry: favorite });
    mount('folder', true);
    await waitFor(() => expect(toggle()).toHaveAttribute('aria-pressed', 'true'));
    fireEvent.click(toggle());
    await waitFor(() => expect(remove).toHaveBeenCalledWith(favorite));
  });

  it('does not present loading or failed reads as an unfavorited directory', async () => {
    let reject!: (error: unknown) => void;
    vi.spyOn(api, 'getFavorites').mockImplementation(() => new Promise((_resolve, fail) => { reject = fail; }));
    mount();
    expect(screen.getByRole('button')).toBeDisabled();
    expect(screen.getByRole('button')).not.toHaveAttribute('aria-pressed');
    await act(async () => reject(new ApiError(503, 'favorites_unavailable')));
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.getAllByRole('button')[0]).toBeDisabled();
    expect(screen.getAllByRole('button')[0]).not.toHaveAttribute('aria-pressed');
  });

  it('captures the target and does not apply late errors to another directory', async () => {
    vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [] });
    let reject!: (error: unknown) => void;
    const add = vi.spyOn(api, 'addFavorite').mockImplementation(() => new Promise((_resolve, fail) => { reject = fail; }));
    const view = mount('folder', true);
    await waitFor(() => expect(toggle()).toBeEnabled());
    fireEvent.click(toggle());
    await waitFor(() => expect(add).toHaveBeenCalledWith('folder', 'folder'));
    view.setDirectory('other');
    expect(toggle()).toBeDisabled();
    await act(async () => reject(new ApiError(409, 'favorite_conflict')));
    await waitFor(() => expect(toggle()).toBeEnabled());
    expect(toggle()).toHaveAttribute('aria-pressed', 'false');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('retains known favorites and shows conflict feedback while refreshing', async () => {
    const get = vi.spyOn(api, 'getFavorites').mockResolvedValue({ ok: true, entries: [favorite] });
    vi.spyOn(api, 'removeFavorite').mockRejectedValue(new ApiError(409, 'favorite_conflict'));
    mount();
    await waitFor(() => expect(toggle()).toBeEnabled());
    fireEvent.click(toggle());
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(toggle()).toHaveAttribute('aria-pressed', 'true');
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  });

  it('shares reads, pending writes, and refreshed state with the panel', async () => {
    let entries: Favorite[] = [];
    const get = vi.spyOn(api, 'getFavorites').mockImplementation(async () => ({ ok: true, entries }));
    let resolve!: (result: Awaited<ReturnType<typeof api.addFavorite>>) => void;
    const add = vi.spyOn(api, 'addFavorite').mockImplementation(() => new Promise(done => { resolve = done; }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><I18nProvider><FavoriteButton directory="folder" /><FavoritesPanel directory="folder" onClose={() => {}} onNavigate={() => {}} /></I18nProvider></QueryClientProvider>);
    const toggles = () => screen.getAllByRole('button', { hidden: true }).filter(button => button.hasAttribute('aria-pressed'));
    await waitFor(() => expect(toggles()).toHaveLength(2));
    expect(get).toHaveBeenCalledTimes(1);
    fireEvent.click(toggles()[0]);
    fireEvent.click(toggles()[1]);
    await waitFor(() => expect(add).toHaveBeenCalledTimes(1));
    expect(toggles().every(button => (button as HTMLButtonElement).disabled)).toBe(true);
    entries = [favorite];
    await act(async () => resolve({ ok: true, entry: favorite }));
    await waitFor(() => expect(toggles().every(button => button.getAttribute('aria-pressed') === 'true')).toBe(true));
    expect(screen.getByText('Saved folder')).toBeVisible();
  });
});
