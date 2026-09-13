import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const putStoredUpload = vi.hoisted(() => vi.fn());
const deleteStoredUpload = vi.hoisted(() => vi.fn().mockResolvedValue(undefined));
const createUpload = vi.hoisted(() => vi.fn());
const getUpload = vi.hoisted(() => vi.fn());
const cancelUpload = vi.hoisted(() => vi.fn());
const completeUpload = vi.hoisted(() => vi.fn());

vi.mock('./storage', () => ({
  putStoredUpload,
  deleteStoredUpload
}));
vi.mock('../api/client', () => ({
  api: { createUpload, getUpload, cancelUpload, completeUpload },
  ApiError: class ApiError extends Error {
    constructor(public readonly status: number, public readonly code: string) {
      super(code);
    }
  },
  xhrUploadPart: vi.fn()
}));

import { ReliableUploadQueue } from './queue';
import type { StoredUpload } from './storage';

function memoryFile(contents: string, name: string, lastModified: number): File {
  const bytes = new TextEncoder().encode(contents);
  return {
    name,
    size: bytes.length,
    lastModified,
    slice(start = 0, end = bytes.length) {
      const part = bytes.slice(start, end);
      return { size: part.length, arrayBuffer: async () => part.buffer.slice(part.byteOffset, part.byteOffset + part.byteLength) } as Blob;
    }
  } as File;
}

const stored: StoredUpload = {
  version: 1,
  id: 'stored-upload',
  token: 'capability',
  scope: 'https://example.test|admin',
  path: 'docs',
  name: 'report.txt',
  size: 3,
  lastModified: 123,
  sha256: '039058c6f2c0cb492c533b0a4d14ef77a7d5eecb14b5610d2b0091d0d64c2f64',
  createdAt: 1
};

beforeEach(() => {
  putStoredUpload.mockClear();
  deleteStoredUpload.mockClear();
  createUpload.mockClear();
  getUpload.mockClear();
  cancelUpload.mockClear();
  completeUpload.mockClear();
});

afterEach(() => {
  putStoredUpload.mockClear();
  deleteStoredUpload.mockClear();
  createUpload.mockClear();
  getUpload.mockClear();
  cancelUpload.mockClear();
  completeUpload.mockClear();
});

describe('reliable upload queue restoration', () => {
  it('reselects equal basenames by full path without creating replacement uploads', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    queue.restore(['a', 'b'].map(child => ({ ...stored, id: child, path: `docs/root/${child}`, relativePath: `root/${child}/report.txt` })));
    const attach = vi.spyOn(queue, 'attachFile').mockResolvedValue(true);
    const file = memoryFile('abc', 'report.txt', 123);
    expect(await queue.restoreDirectoryFiles([{ file, relativePath: 'root/b/report.txt' }], 'docs')).toBe(true);
    expect(attach).toHaveBeenCalledWith('b', file);
    expect(queue.snapshot()).toHaveLength(2);
    expect(createUpload).not.toHaveBeenCalled();
    attach.mockRestore();
  });
  it('does not create a new upload when a folder reselected source fails identity validation', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    queue.restore([{ ...stored, path: 'docs/root', relativePath: 'root/report.txt' }]);
    expect(await queue.restoreDirectoryFiles([{ file: memoryFile('changed', 'report.txt', 123), relativePath: 'root/report.txt' }], 'docs')).toBe(false);
    expect(queue.snapshot()).toHaveLength(1);
    expect(createUpload).not.toHaveBeenCalled();
  });

  it('does not turn a new single-file selection into a restored upload attachment', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    queue.restore([stored]);
    const fresh = memoryFile('new', 'new.txt', 456);
    createUpload.mockRejectedValue(new Error('stop worker'));

    queue.add([fresh]);
    await vi.waitFor(() => expect(queue.snapshot().find((item) => item.name === 'new.txt')?.phase).toBe('failed'));

    const items = queue.snapshot();
    expect(items).toHaveLength(2);
    expect(items.find((item) => item.id === stored.id)).toMatchObject({ phase: 'reselect' });
    expect(items.find((item) => item.name === 'new.txt')).toMatchObject({ file: fresh });
    expect(items.find((item) => item.name === 'new.txt')?.phase).not.toBe('reselect');
  });

  it('persists only the approved v1 metadata after hashing a new upload', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    const file = memoryFile('new', 'new.txt', 456);

    queue.add([file]);
    await vi.waitFor(() => expect(putStoredUpload).toHaveBeenCalled());

    const metadata = putStoredUpload.mock.calls.map(([value]) => value).find((value) => value.name === 'new.txt');
    expect(metadata).toEqual(expect.objectContaining({
      version: 1,
      id: expect.any(String),
      token: expect.any(String),
      scope: stored.scope,
      path: 'docs',
      name: 'new.txt',
      size: 3,
      lastModified: 456,
      sha256: expect.any(String),
      createdAt: expect.any(Number)
    }));
    expect(metadata).not.toHaveProperty('file');
    expect(metadata).not.toHaveProperty('phase');
  });

  it('cancels a reservation created while the create response is still pending', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    const file = memoryFile('new', 'new.txt', 456);
    let resolveCreate!: (value: { upload: { state: 'active'; received_bytes: number; part_count: number; chunk_bytes: number; received: [] } }) => void;
    createUpload.mockReturnValue(new Promise((resolve) => { resolveCreate = resolve; }));
    cancelUpload.mockResolvedValue({ ok: true });

    queue.add([file]);
    await vi.waitFor(() => expect(createUpload).toHaveBeenCalled());
    const cancel = queue.cancel(queue.snapshot().find((item) => item.name === 'new.txt')!.id);
    resolveCreate({ upload: { state: 'active', received_bytes: 0, part_count: 1, chunk_bytes: 8 * 1024 * 1024, received: [] } });
    await cancel;

    expect(cancelUpload).toHaveBeenCalledOnce();
    expect(queue.snapshot().find((item) => item.name === 'new.txt')).toMatchObject({ phase: 'cancelled' });
  });

  it('removes a cancelled upload without allowing a delayed worker update to restore metadata', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    queue.restore([stored]);
    getUpload.mockResolvedValue({ upload: { state: 'cancelled' } });

    const staleItem = queue.snapshot()[0];
    await queue.remove(stored.id);
    putStoredUpload.mockClear();
    // Simulate a worker retaining an item reference while cancellation removes it.
    (queue as unknown as { update: (item: unknown, patch: unknown) => void }).update(staleItem, { phase: 'failed' });

    expect(queue.snapshot()).toEqual([]);
    expect(deleteStoredUpload).toHaveBeenCalledWith(stored.id);
    expect(putStoredUpload).not.toHaveBeenCalled();
  });

  it('rejects an explicitly attached source that does not match stored metadata', async () => {
    const queue = new ReliableUploadQueue(stored.scope, () => 'docs');
    queue.restore([stored]);

    await expect(queue.attachFile(stored.id, memoryFile('new', 'other.txt', 123))).resolves.toBe(false);
    expect(queue.snapshot()).toEqual([expect.objectContaining({ id: stored.id, phase: 'reselect' })]);
    expect(putStoredUpload).not.toHaveBeenCalled();
  });
});
