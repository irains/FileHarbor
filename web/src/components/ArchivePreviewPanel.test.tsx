import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api, type FileEntry } from '../api/client';
import { I18nProvider } from '../i18n';
import { ArchivePreviewPanel } from './ArchivePreviewPanel';
import { entryMenuActions } from './entryActions';

const entry: FileEntry = { name: 'sample.zip', path: 'sample.zip', kind: 'file', sizeBytes: 100, modifiedAt: '', mode: '', isArchive: true, previewable: false, editable: false, version: 'version' };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider><ArchivePreviewPanel entry={entry} onClose={() => {}} /></I18nProvider></QueryClientProvider>);
}

describe('ArchivePreviewPanel', () => {
  it('shows metadata as text and distinguishes unknown sizes', async () => {
    const request = vi.spyOn(api, 'previewArchive').mockResolvedValue({ ok: true, preview: { entries: [{ name: '<img src=x>/data', kind: 'file' }], complete: true, truncated: false, verification: 'metadata_only', entries_scanned: 1 } });
    mount();
    expect(await screen.findByText('<img src=x>/data')).toBeVisible();
    expect(screen.getByText('Size unknown')).toBeVisible();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
    expect(request).toHaveBeenCalledWith('sample.zip', 'version', expect.any(AbortSignal));
    expect(screen.getByText(/Payload integrity is not fully verified/)).toBeVisible();
  });
  it('groups implicit directories and supports keyboard-native disclosure', async () => {
    vi.spyOn(api, 'previewArchive').mockResolvedValue({ ok: true, preview: { entries: [{ name: 'folder/nested/data.txt', kind: 'file', size: 4 }], complete: true, truncated: false, verification: 'metadata_only', entries_scanned: 1 } });
    mount();
    const nested = await screen.findByText('folder/nested', { selector: 'summary' });
    expect(screen.getByText('folder/nested/data.txt')).not.toBeVisible();
    fireEvent.click(nested);
    expect(screen.getByText('folder/nested/data.txt')).toBeVisible();
    expect(screen.getByText('Declared size: 4 B')).toBeVisible();
  });
  it('marks truncated results and permits read-only preview', async () => {
    vi.spyOn(api, 'previewArchive').mockResolvedValue({ ok: true, preview: { entries: [], complete: false, truncated: true, reason: 'archive_limit_exceeded', verification: 'metadata_only', entries_scanned: 0 } });
    mount();
    expect(await screen.findByText(/This preview is incomplete/)).toBeVisible();
    const actions = entryMenuActions(entry, false, false).map((action) => action.name);
    expect(actions).toContain('archivePreview');
    expect(actions).not.toContain('extract');
  });
});
