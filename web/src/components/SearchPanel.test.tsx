import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { api, type SearchResult } from '../api/client';
import { I18nProvider } from '../i18n';
import { SearchPanel } from './SearchPanel';

const result: SearchResult = { entries: [{ name: 'report.txt', path: 'nested/report.txt', parent: 'nested', kind: 'file', size: 4, modified: '2026-09-13T00:00:00Z', version: 'v' }], visited: 140, skipped: 0, incomplete: false };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
function mount(onNavigate = vi.fn()) {
  return render(<I18nProvider><SearchPanel directory="" onClose={() => {}} onNavigate={onNavigate} /></I18nProvider>);
}

describe('SearchPanel', () => {
  it('submits explicit scope and filters and navigates to the parent', async () => {
    const search = vi.spyOn(api, 'search').mockResolvedValue({ ok: true, result });
    const navigate = vi.fn();
    mount(navigate);
    fireEvent.change(screen.getByLabelText('Name contains'), { target: { value: 'report' } });
    fireEvent.change(screen.getByLabelText('Min bytes'), { target: { value: '4' } });
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    expect(await screen.findByText('nested/report.txt')).toBeVisible();
    expect(search).toHaveBeenCalledWith({ path: '', recursive: 'true', name: 'report', min_size: '4' }, expect.any(AbortSignal));
    fireEvent.click(screen.getByRole('button', { name: 'Open containing folder' }));
    expect(navigate).toHaveBeenCalledWith('nested');
  });
  it('cancels superseded requests and ignores late responses', async () => {
    let resolveFirst!: (value: { ok: true; result: SearchResult }) => void;
    const search = vi.spyOn(api, 'search').mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; })).mockResolvedValueOnce({ ok: true, result: { ...result, entries: [], incomplete: true } });
    mount();
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    const signal = search.mock.calls[0][1]!;
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    expect(signal.aborted).toBe(true);
    expect(await screen.findByText('No matching entries.')).toBeVisible();
    resolveFirst({ ok: true, result });
    await waitFor(() => expect(screen.queryByText('nested/report.txt')).not.toBeInTheDocument());
    expect(screen.getByText(/Search is incomplete/)).toBeVisible();
  });
  it('aborts the request when closed', () => {
    const search = vi.spyOn(api, 'search').mockImplementation(() => new Promise(() => {}));
    const view = mount();
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    const signal = search.mock.calls[0][1]!;
    view.unmount();
    expect(signal.aborted).toBe(true);
  });
});
