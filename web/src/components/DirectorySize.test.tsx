import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api, type SizeScan } from '../api/client';
import { I18nProvider } from '../i18n';
import { DirectorySize } from './DirectorySize';

const finished: SizeScan = { id: 'scan', path: 'folder', state: 'succeeded', result: { bytes: 8, files: 2, directories: 2, visited: 4, skipped: 0, incomplete: false, scanned_at: '2026-09-13T00:00:00Z' } };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider><DirectorySize path="folder" /></I18nProvider></QueryClientProvider>);
}

describe('DirectorySize', () => {
  it('does not scan until requested and refreshes a reused result ID', async () => {
    const start = vi.spyOn(api, 'startSizeScan').mockResolvedValueOnce({ ok: true, scan: finished }).mockResolvedValueOnce({ ok: true, scan: { ...finished, result: { ...finished.result!, bytes: 12 } } });
    vi.spyOn(api, 'getSizeScan').mockResolvedValue({ ok: true, scan: finished });
    mount();
    expect(start).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Calculate folder size' }));
    expect(await screen.findByText('8 B')).toBeVisible();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Calculate folder size' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Calculate folder size' }));
    expect(await screen.findByText('12 B')).toBeVisible();
    expect(start).toHaveBeenCalledWith('folder');
  });
  it('reports incomplete results without presenting them as complete', async () => {
    const scan = { ...finished, result: { ...finished.result!, incomplete: true, skipped: 1 } };
    vi.spyOn(api, 'startSizeScan').mockResolvedValue({ ok: true, scan });
    vi.spyOn(api, 'getSizeScan').mockResolvedValue({ ok: true, scan });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Calculate folder size' }));
    expect(await screen.findByText('Some items were not counted.')).toBeVisible();
  });
});
