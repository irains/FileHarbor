import type { DirectoryFile } from './directoryEntries';
import { sha256 } from '@noble/hashes/sha256';
import { bytesToHex } from '@noble/hashes/utils';
import { api, ApiError, xhrUploadPart, type UploadStatus } from '../api/client';
import { deleteStoredUpload, putStoredUpload, type StoredUpload } from './storage';

export function randomHex(bytes = 32): string {
  const output = new Uint8Array(bytes);
  crypto.getRandomValues(output);
  return bytesToHex(output);
}

export function receivedIndexes(status: UploadStatus): Set<number> {
  const indexes = new Set<number>();
  for (const range of status.received) for (let index = range.start; index <= range.end; index += 1) indexes.add(index);
  return indexes;
}

export async function hashBlob(blob: Blob, sliceBytes = 1024 * 1024): Promise<string> {
  const hash = sha256.create();
  for (let start = 0; start < blob.size; start += sliceBytes) hash.update(new Uint8Array(await blob.slice(start, Math.min(blob.size, start + sliceBytes)).arrayBuffer()));
  return bytesToHex(hash.digest());
}

export type UploadPhase = 'reselect' | 'waiting' | 'hashing' | 'uploading' | 'paused' | 'completed' | 'failed' | 'cancelled';
export interface QueueItem extends StoredUpload {
  file?: File;
  phase: UploadPhase;
  progress: number;
  receivedBytes: number;
  error?: string;
  status?: UploadStatus;
}

type Listener = () => void;

export class ReliableUploadQueue {
  private items = new Map<string, QueueItem>();
  private listeners = new Set<Listener>();
  private active = 0;
  private activeParts = new Map<string, AbortController>();
  private resolving = new Map<string, Promise<UploadStatus>>();
  private cancelling = new Set<string>();
  private readonly maxWorkers = 2;

  constructor(private readonly scope: string, private readonly destination: () => string) {}
  subscribe(listener: Listener) { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; }
  snapshot = () => [...this.items.values()].sort((a, b) => a.createdAt - b.createdAt);
  private notify() { this.listeners.forEach((listener) => listener()); }
  private persist(item: QueueItem) {
    if (!this.items.has(item.id)) return;
    const stored: StoredUpload = {
      version: item.version,
      id: item.id,
      token: item.token,
      scope: item.scope,
      path: item.path,
      name: item.name,
      relativePath: item.relativePath,
      size: item.size,
      lastModified: item.lastModified,
      sha256: item.sha256,
      createdAt: item.createdAt
    };
    void putStoredUpload(stored);
  }
  private update(item: QueueItem, patch: Partial<QueueItem>) {
    if (!this.items.has(item.id)) return;
    Object.assign(item, patch);
    if (item.sha256) this.persist(item);
    this.notify();
  }

  restore(items: StoredUpload[]) {
    for (const stored of items) this.items.set(stored.id, { ...stored, phase: 'reselect', progress: 0, receivedBytes: 0 });
    this.notify();
  }

  add(files: File[]) {
    this.addDirectoryFiles(files.map(file => ({ file, relativePath: file.name })), this.destination());
  }

  addDirectoryFiles(files: DirectoryFile[], destination: string) {
    for (const { file, relativePath } of files) {
      const parent = relativePath.split('/').slice(0, -1).join('/');
      const item: QueueItem = {
        version: 1, id: randomHex(16), token: randomHex(), scope: this.scope,
        path: [destination, parent].filter(Boolean).join('/'), name: file.name, relativePath, size: file.size,
        lastModified: file.lastModified, sha256: '', createdAt: Date.now(), phase: 'waiting', receivedBytes: 0, file, progress: 0
      };
      this.items.set(item.id, item);
    }
    this.notify(); this.pump();
  }

  async restoreDirectoryFiles(files: DirectoryFile[], destination: string) {
    const additions: DirectoryFile[] = [];
    let mismatched = false;
    for (const selected of files) {
      const parent = selected.relativePath.split('/').slice(0, -1).join('/');
      const path = [destination, parent].filter(Boolean).join('/');
      const matches = this.snapshot().filter(item => item.path === path && item.relativePath === selected.relativePath && item.name === selected.file.name && !['completed', 'cancelled'].includes(item.phase));
      if (matches.length > 1) { mismatched = true; continue; }
      const existing = matches[0];
      if (!existing) { additions.push(selected); continue; }
      if (existing.file) continue;
      if (!await this.attachFile(existing.id, selected.file)) mismatched = true;
    }
    this.addDirectoryFiles(additions, destination);
    return !mismatched;
  }

  async attachFile(id: string, file: File) {
    const item = this.items.get(id);
    if (!item || item.name !== file.name || item.size !== file.size || item.lastModified !== file.lastModified) return false;
    if (file.webkitRelativePath && item.relativePath && file.webkitRelativePath !== item.relativePath) return false;
    this.update(item, { phase: 'hashing', error: undefined });
    const digest = await hashBlob(file);
    if (item.phase === 'cancelled' || item.phase === 'paused') return false;
    if (digest !== item.sha256) {
      this.update(item, { phase: 'reselect', error: 'source_changed' });
      return false;
    }
    this.update(item, { file, phase: 'waiting', error: undefined }); this.pump(); return true;
  }

  pause(id: string) {
    const item = this.items.get(id);
    if (!item || ['completed', 'cancelled'].includes(item.phase)) return;
    this.activeParts.get(id)?.abort();
    this.update(item, { phase: 'paused' });
  }
  resume(id: string) {
    const item = this.items.get(id);
    if (item?.file && ['paused', 'failed', 'waiting'].includes(item.phase)) { this.update(item, { phase: 'waiting', error: undefined }); this.pump(); }
  }
  async cancel(id: string) {
    const item = this.items.get(id); if (!item || ['completed', 'cancelled'].includes(item.phase)) return;
    if (this.cancelling.has(id)) return;
    this.cancelling.add(id);
    this.activeParts.get(id)?.abort();
    this.update(item, { phase: 'paused', error: undefined });
    try {
      let status = item.status;
      if (!status || status.state === 'active') {
        const resolving = this.resolving.get(id);
        status = resolving ? await resolving : (await api.getUpload(id, item.token)).upload;
      }
      if (status.state === 'completed') {
        this.update(item, { status, phase: 'completed', progress: 1, receivedBytes: item.size });
        return;
      }
      if (status.state === 'cancelled') {
        this.update(item, { status, phase: 'cancelled' });
        return;
      }
      await api.cancelUpload(id, item.token);
      this.update(item, { status, phase: 'cancelled' });
    } catch (error) {
      const code = error instanceof ApiError ? error.code : 'upload_failed';
      if (code === 'upload_cancelled' || code === 'upload_not_found') {
        this.update(item, { phase: 'cancelled' });
        return;
      }
      if (code === 'upload_conflict') {
        try {
          const status = (await api.getUpload(id, item.token)).upload;
          if (status.state === 'completed') {
            this.update(item, { status, phase: 'completed', progress: 1, receivedBytes: item.size });
            return;
          }
        } catch { /* Preserve the conflict if reconciliation cannot establish a terminal result. */ }
      }
      this.update(item, { phase: 'failed', error: code });
    } finally {
      this.cancelling.delete(id);
    }
  }
  async remove(id: string) {
    const item = this.items.get(id);
    if (!item) return;
    if (!['completed', 'cancelled'].includes(item.phase)) {
      await this.cancel(id);
      if (!['completed', 'cancelled'].includes(item.phase)) return;
    }
    this.activeParts.get(id)?.abort();
    this.items.delete(id);
    await deleteStoredUpload(id);
    this.notify();
  }

  private pump() {
    while (this.active < this.maxWorkers) {
      const item = this.snapshot().find((candidate) => candidate.phase === 'waiting' && candidate.file);
      if (!item) return;
      this.active += 1;
      void this.run(item).finally(() => { this.active -= 1; this.pump(); });
    }
  }

  private async resolveStatus(item: QueueItem): Promise<UploadStatus> {
    if (item.status?.state === 'active') return (await api.getUpload(item.id, item.token)).upload;
    const pending = (async () => {
      try {
        return (await api.createUpload({ path: item.path, name: item.name, size: item.size, sha256: item.sha256 }, item.id, item.token)).upload;
      } catch (error) {
        if (error instanceof ApiError && [409, 422].includes(error.status)) return (await api.getUpload(item.id, item.token)).upload;
        throw error;
      }
    })();
    this.resolving.set(item.id, pending);
    try {
      return await pending;
    } finally {
      if (this.resolving.get(item.id) === pending) this.resolving.delete(item.id);
    }
  }

  private async run(item: QueueItem) {
    try {
      const file = item.file; if (!file) return;
      if (!item.sha256) {
        this.update(item, { phase: 'hashing' });
        const sha256 = await hashBlob(file);
        if (item.phase === 'paused' || item.phase === 'cancelled') return;
        this.update(item, { sha256 });
      }
      this.update(item, { phase: 'uploading' });
      let status = await this.resolveStatus(item);
      if (item.phase === 'paused' || item.phase === 'cancelled') {
        if (item.phase === 'paused') await this.cancel(item.id);
        return;
      }
      this.update(item, { status, receivedBytes: status.received_bytes, progress: item.size ? status.received_bytes / item.size : 0 });
      const received = receivedIndexes(status);
      for (let index = 0; index < status.part_count; index += 1) {
        if (['paused', 'cancelled'].includes(item.phase)) return;
        if (received.has(index)) continue;
        const start = index * status.chunk_bytes;
        const part = file.slice(start, Math.min(file.size, start + status.chunk_bytes));
        const digest = await hashBlob(part);
        await this.uploadPart(item, { index, part, digest, status, received, file });
        received.add(index);
      }
      status = await this.retry(() => api.completeUpload(item.id, item.token)).then((result) => result.upload);
      this.update(item, { status, receivedBytes: file.size, progress: 1, phase: 'completed' });
    } catch (error) {
      if (item.phase === 'paused' || item.phase === 'cancelled') return;
      const code = error instanceof ApiError ? error.code : error instanceof Error ? error.message : 'upload_failed';
      this.update(item, { phase: 'failed', error: code });
    }
  }

  private async uploadPart(item: QueueItem, input: { index: number; part: Blob; digest: string; status: UploadStatus; received: Set<number>; file: File }) {
    for (let attempt = 0; attempt < 3; attempt += 1) {
      const controller = new AbortController();
      this.activeParts.set(item.id, controller);
      try {
        await xhrUploadPart({
          id: item.id,
          token: item.token,
          index: input.index,
          bytes: input.part,
          sha256: input.digest,
          signal: controller.signal,
          onProgress: (loaded) => {
            const completed = input.status.received_bytes + [...input.received].filter((partIndex) => partIndex < input.index).reduce((total, partIndex) => total + Math.min(input.status.chunk_bytes, input.file.size - partIndex * input.status.chunk_bytes), 0);
            this.update(item, { receivedBytes: completed + loaded, progress: input.file.size ? (completed + loaded) / input.file.size : 1 });
          }
        });
        return;
      } catch (error) {
        if (item.phase === 'paused' || item.phase === 'cancelled') throw error;
        if (!this.retryable(error) || attempt === 2) throw error;
        try {
          const reconciled = (await api.getUpload(item.id, item.token)).upload;
          item.status = reconciled;
          if (receivedIndexes(reconciled).has(input.index)) return;
        } catch { /* Retry after the established bounded delay when reconciliation is ambiguous. */ }
        await delay([500, 1000, 2000][attempt]);
      } finally {
        if (this.activeParts.get(item.id) === controller) this.activeParts.delete(item.id);
      }
    }
  }

  private retryable(error: unknown) {
    if (!(error instanceof ApiError)) return true;
    return error.status === 0 || error.status === 408 || error.status >= 500;
  }
  private async retry<T>(operation: () => Promise<T>): Promise<T> {
    let lastError: unknown;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      try { return await operation(); }
      catch (error) { lastError = error; if (!this.retryable(error) || attempt === 2) throw error; await delay([500, 1000, 2000][attempt]); }
    }
    throw lastError ?? new Error('upload_failed');
  }
}

function delay(milliseconds: number) { return new Promise((resolve) => setTimeout(resolve, milliseconds)); }
