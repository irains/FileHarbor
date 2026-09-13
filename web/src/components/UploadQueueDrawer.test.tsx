import { describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { I18nProvider } from '../i18n';
import { UploadQueueDrawer } from './UploadQueueDrawer';

const snapshot = vi.fn<() => unknown[]>(() => []);
const queueDestinations: Array<() => string> = [];
let queueListener: (() => void) | undefined;

vi.mock('../uploads/storage', () => ({
  uploadScope: () => 'scope',
  listStoredUploads: vi.fn().mockResolvedValue([])
}));

vi.mock('../uploads/queue', () => ({
  ReliableUploadQueue: class {
    constructor(_scope: string, destination: () => string) { queueDestinations.push(destination); }
    subscribe(listener: () => void) { queueListener = listener; listener(); return () => {}; }
    snapshot() { return snapshot(); }
    restore() {}
    add() {}
  }
}));

const completed = { id: 'complete', phase: 'completed', progress: 1, receivedBytes: 12, name: 'report.txt', path: 'reports', size: 12, createdAt: 1 };
const cancelled = { id: 'cancelled', phase: 'cancelled', progress: 0, receivedBytes: 0, name: 'draft.txt', path: 'drafts', size: 8, createdAt: 2 };

const renderDrawer = (onAllComplete = vi.fn()) => render(<I18nProvider><UploadQueueDrawer open onClose={() => {}} destination="" username="operator" onAllComplete={onAllComplete} /></I18nProvider>);

describe('UploadQueueDrawer', () => {
  it('exposes a keyboard-operable drop zone that opens the file picker', () => {
    snapshot.mockReturnValue([]);
    renderDrawer();
    const input = document.querySelector('input[type="file"]:not([webkitdirectory])') as HTMLInputElement;
    expect(input).toBeTruthy();
    const click = vi.spyOn(input, 'click');
    const dropZone = screen.getByRole('button', { name: 'Drop files here or choose files to upload' });
    fireEvent.keyDown(dropZone, { key: 'Enter' });
    expect(click).toHaveBeenCalledOnce();
  });

  it('does not announce cancellation as a successful completed queue', async () => {
    snapshot.mockReturnValue([cancelled]);
    const onAllComplete = vi.fn();
    await act(async () => { renderDrawer(onAllComplete); });
    expect(screen.queryByText('All uploads are complete.')).not.toBeInTheDocument();
    expect(onAllComplete).not.toHaveBeenCalled();
  });

  it('keeps one queue while its destination callback follows navigation', async () => {
    snapshot.mockReturnValue([]);
    queueDestinations.length = 0;
    const onAllComplete = vi.fn();
    const view = render(<I18nProvider><UploadQueueDrawer open onClose={() => {}} destination="first" username="operator" onAllComplete={onAllComplete} /></I18nProvider>);
    await act(async () => {});
    expect(queueDestinations).toHaveLength(1);
    expect(queueDestinations[0]()).toBe('first');

    view.rerender(<I18nProvider><UploadQueueDrawer open onClose={() => {}} destination="second" username="operator" onAllComplete={onAllComplete} /></I18nProvider>);
    await act(async () => {});
    expect(queueDestinations).toHaveLength(1);
    expect(queueDestinations[0]()).toBe('second');
  });

  it('retains completed destinations when their rows are removed before the queue finishes', async () => {
    const uploading = { ...cancelled, id: 'uploading', path: 'drafts', phase: 'uploading' };
    snapshot.mockReturnValue([completed, uploading]);
    const onAllComplete = vi.fn();
    renderDrawer(onAllComplete);
    await act(async () => {});
    expect(onAllComplete).not.toHaveBeenCalled();

    snapshot.mockReturnValue([{ ...completed, id: 'draft-complete', path: 'drafts' }]);
    await act(async () => { queueListener?.(); });
    expect(onAllComplete).toHaveBeenCalledOnce();
    expect(onAllComplete).toHaveBeenCalledWith(['reports', 'drafts']);
  });

  it('starts a new completion cycle without retaining prior completed rows', async () => {
    snapshot.mockReturnValue([completed]);
    const onAllComplete = vi.fn();
    renderDrawer(onAllComplete);
    await waitFor(() => expect(onAllComplete).toHaveBeenCalledWith(['reports']));

    snapshot.mockReturnValue([completed, { ...cancelled, id: 'new-upload', path: 'drafts', phase: 'uploading' }]);
    await act(async () => { queueListener?.(); });
    expect(onAllComplete).toHaveBeenCalledOnce();

    snapshot.mockReturnValue([completed, { ...completed, id: 'new-complete', path: 'drafts' }]);
    await act(async () => { queueListener?.(); });
    expect(onAllComplete).toHaveBeenCalledTimes(2);
    expect(onAllComplete).toHaveBeenLastCalledWith(['reports', 'drafts']);
  });

  it('announces a terminal queue only once across callback rerenders', async () => {
    snapshot.mockReturnValue([completed, cancelled]);
    const onAllComplete = vi.fn();
    const view = renderDrawer(onAllComplete);
    await waitFor(() => expect(screen.getByText('All uploads are complete.')).toBeInTheDocument());
    expect(onAllComplete).toHaveBeenCalledOnce();
    expect(onAllComplete).toHaveBeenCalledWith(['reports']);

    view.rerender(<I18nProvider><UploadQueueDrawer open onClose={() => {}} destination="" username="operator" onAllComplete={() => onAllComplete()} /></I18nProvider>);
    await act(async () => {});
    expect(onAllComplete).toHaveBeenCalledOnce();
  });
});
