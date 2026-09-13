import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { I18nProvider } from '../i18n';
import { ExtractArchiveDialog } from './ExtractArchiveDialog';
const submit = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
vi.mock('../tasks/TaskProvider', () => ({ useTasks: () => ({ submit }) }));
vi.mock('./FolderDestinationPicker', () => ({ FolderDestinationPicker: ({ onChange }: { onChange: (path: string) => void }) => <button onClick={() => onChange('')}>Pick root</button> }));
afterEach(() => { cleanup(); submit.mockClear(); });
const entry = { name: 'sample.tar.gz', path: 'folder/sample.tar.gz', kind: 'file' as const, sizeBytes: 1, modifiedAt: '', mode: '', version: 'version', isArchive: true, previewable: false, editable: false };
describe('ExtractArchiveDialog', () => {
 it('defaults to a new folder and strips compound suffixes', async () => {
  const close = vi.fn(); render(<I18nProvider><ExtractArchiveDialog entry={entry} onClose={close} /></I18nProvider>);
  expect(screen.getByRole('radio', { name: 'New folder beside archive' })).toBeChecked();
  expect(screen.getByLabelText('Folder name')).toHaveValue('sample');
  fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  await waitFor(() => expect(submit).toHaveBeenCalledWith({ kind: 'extract', path: entry.path, version: 'version', target: { mode: 'new_folder', name: 'sample' } }));
 });
 it('sends the explicit managed root when chosen', async () => {
  render(<I18nProvider><ExtractArchiveDialog entry={entry} onClose={() => {}} /></I18nProvider>);
  fireEvent.click(screen.getByRole('radio', { name: 'Choose existing folder' }));
  fireEvent.click(screen.getByText('Pick root'));
  fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  await waitFor(() => expect(submit).toHaveBeenCalledWith(expect.objectContaining({ target: { mode: 'chosen', directory: '' } })));
 });
});
