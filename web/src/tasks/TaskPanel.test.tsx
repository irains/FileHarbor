import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { type FileJob } from '../api/client';
import { TaskPanel } from './TaskPanel';
const cancel = vi.hoisted(() => vi.fn().mockResolvedValue({}));
vi.mock('../api/client', async original => ({ ...await original<object>(), api: { cancelJob: cancel } }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
const job = { id: 'job', kind: 'copy', sources: [{ path: 'source', version: 'v' }], destination: 'out', target: { mode: '' }, state: 'running', phase: 'writing', bytes: 8, items: 1, published: [], cancel_requested: false } as FileJob;
function mount(value: FileJob, canMutate = true) {
 const retry = vi.fn().mockResolvedValue(undefined);
 render(<MemoryRouter><I18nProvider><TaskPanel open onClose={() => {}} jobs={[value]} canMutate={canMutate} loading={false} failed={false} refresh={vi.fn().mockResolvedValue({})} retry={retry} /></I18nProvider></MemoryRouter>);
 return retry;
}
describe('TaskPanel', () => {
 it('requests cancellation without claiming that running work has stopped', async () => {
  mount(job); fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
  await waitFor(() => expect(cancel).toHaveBeenCalledWith('job'));
  expect(screen.getByText('Copy · Running')).toBeVisible();
 });
 it('disables repeated cancellation requests', () => {
  mount({ ...job, cancel_requested: true });
  expect(screen.getByRole('button', { name: 'Cancellation requested' })).toBeDisabled();
 });
 it('requires confirmation before retrying a partial operation', async () => {
  const retry = mount({ ...job, state: 'partial', intent: 'out/uncertain', published: ['out/complete'] });
  expect(screen.getByText(/Publication outcome is uncertain/)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  expect(retry).not.toHaveBeenCalled();
  expect(screen.getByText(/Existing outputs remain/)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Confirm' }));
  await waitFor(() => expect(retry).toHaveBeenCalledOnce());
 });
 it('does not offer retries without mutation capability', () => {
  mount({ ...job, state: 'interrupted' }, false);
  expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument();
 });
});
