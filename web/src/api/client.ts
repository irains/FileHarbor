import { baseUrl } from '../runtime';

export interface ApiErrorBody { ok: false; code: string }
export class ApiError extends Error {
  constructor(public readonly status: number, public readonly code: string) {
    super(code);
    this.name = 'ApiError';
  }
}

export interface SessionCapabilities {
  browse: boolean;
  upload: boolean;
  mutate: boolean;
  editor_save: boolean;
}

export interface SessionBootstrapResponse {
  ok: true;
  session: { username: string; csrf_token: string; expires_at: string };
  base_path: string;
  locale: 'en' | 'zh-CN';
  capabilities: SessionCapabilities;
}

export interface BrowserSession {
  username: string;
  csrfToken: string;
  expiresAt: string;
  basePath: string;
  language: 'en' | 'zh';
  capabilities: {
    browse: boolean;
    upload: boolean;
    mutate: boolean;
    editorSave: boolean;
  };
}

export function normalizeSession(response: SessionBootstrapResponse): BrowserSession {
  return {
    username: response.session.username,
    csrfToken: response.session.csrf_token,
    expiresAt: response.session.expires_at,
    basePath: response.base_path,
    language: response.locale === 'zh-CN' ? 'zh' : 'en',
    capabilities: {
      browse: response.capabilities.browse,
      upload: response.capabilities.upload,
      mutate: response.capabilities.mutate,
      editorSave: response.capabilities.editor_save
    }
  };
}

export interface SizeScan {
  id: string; path: string; state: 'running' | 'succeeded' | 'failed' | 'cancelled'; code?: string;
  result?: { bytes: number; files: number; directories: number; scanned_at: string; visited: number; skipped: number; incomplete: boolean; reason?: string };
}

export interface Favorite {
  id: string; path: string; label: string; revision: number; created_at: string;
  availability?: 'available' | 'missing' | 'not_directory' | 'unavailable';
}

export interface SearchResult {
  entries: Array<{ name: string; path: string; parent: string; kind: 'file' | 'directory'; size: number; modified: string; version: string }>;
  visited: number; skipped: number; incomplete: boolean; reason?: string;
}

export interface ArchivePreview {
  entries: Array<{ name: string; kind: 'file' | 'directory'; size?: number }>;
  complete: boolean; truncated: boolean; reason?: string; verification: 'metadata_only'; entries_scanned: number;
}

export interface Directory { name: string; path: string }
export interface Properties {
  name: string; path: string; kind: 'file' | 'directory'; extension?: string; size: number;
  modified: number; mode: string; entry_count?: number; incomplete?: boolean;
}

export interface FileEntry {
  name: string;
  path: string;
  kind: 'file' | 'directory';
  sizeBytes: number;
  modifiedAt: string;
  mode: string;
  extension?: string;
  isArchive: boolean;
  previewable: boolean;
  editable: boolean;
  version: string;
}

export interface ListingResponse {
  ok: true;
  directory: {
    path: string;
    parent_path: string | null;
    listing_token: string;
    entries: Array<{
      name: string;
      path: string;
      kind: 'file' | 'directory';
      size_bytes: number;
      modified_at: string;
      mode: string;
      extension?: string;
      is_archive: boolean;
      previewable: boolean;
      editable: boolean;
      version: string;
    }>;
    truncated: boolean;
  };
  disk?: { total_bytes: number; free_bytes: number; used_percent: number };
}

export interface DirectoryListing {
  path: string;
  parentPath: string | null;
  listingToken: string;
  entries: FileEntry[];
  truncated: boolean;
  disk?: { totalBytes: number; freeBytes: number; usedPercent: number };
}

export function normalizeListing(response: ListingResponse): DirectoryListing {
  return {
    path: response.directory.path,
    parentPath: response.directory.parent_path,
    listingToken: response.directory.listing_token,
    truncated: response.directory.truncated,
    entries: response.directory.entries.map((entry) => ({
      name: entry.name,
      path: entry.path,
      kind: entry.kind,
      sizeBytes: entry.size_bytes,
      modifiedAt: entry.modified_at,
      mode: entry.mode,
      extension: entry.extension,
      isArchive: entry.is_archive,
      previewable: entry.previewable,
      editable: entry.editable,
      version: entry.version
    })),
    disk: response.disk && {
      totalBytes: response.disk.total_bytes,
      freeBytes: response.disk.free_bytes,
      usedPercent: response.disk.used_percent
    }
  };
}

export interface TrashEntry {
  id: string;
  name: string;
  originalPath: string;
  kind: 'file' | 'directory';
  sizeBytes: number;
  deletedAt: string;
}

export interface TrashPage {
  entries: TrashEntry[];
  nextCursor?: string;
}

interface TrashResponse {
  ok: true;
  entries: Array<{
    id: string;
    name: string;
    original_path: string;
    kind: 'file' | 'directory';
    size_bytes: number;
    deleted_at: string;
  }>;
  next_cursor?: string;
}

function normalizeTrash(response: TrashResponse): TrashPage {
  return {
    entries: response.entries.map((entry) => ({
      id: entry.id,
      name: entry.name,
      originalPath: entry.original_path,
      kind: entry.kind,
      sizeBytes: entry.size_bytes,
      deletedAt: entry.deleted_at
    })),
    nextCursor: response.next_cursor || undefined
  };
}

export interface EditorDocument {
  path: string;
  name: string;
  content?: string;
  sizeBytes: number;
  modifiedAt: string;
  extension: string;
  version: string;
}

export interface UploadRange { start: number; end: number }
export interface UploadStatus {
  id: string; path: string; name: string; size: number; chunk_bytes: number; part_count: number;
  received: UploadRange[]; received_bytes: number; state: 'active' | 'finalizing' | 'completed' | 'cancelled';
  expires_at: string; final_sha256?: string; final_path?: string;
}

const jsonHeaders = { Accept: 'application/json' };
let csrfToken = '';
let unauthenticatedHandler: (() => void) | undefined;

export function setCSRFToken(value: string) { csrfToken = value; }
export function setUnauthenticatedHandler(handler?: () => void) { unauthenticatedHandler = handler; }

function csrfHeaders(): HeadersInit {
  return csrfToken ? { 'X-CSRF-Token': csrfToken } : {};
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(baseUrl(path), { credentials: 'same-origin', ...init });
  if (!response.ok) {
    let code = `http_${response.status}`;
    try { code = (await response.json() as ApiErrorBody).code ?? code; } catch { /* non-JSON response */ }
    if (response.status === 401) unauthenticatedHandler?.();
    throw new ApiError(response.status, code);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export interface FileJob {
 id: string; kind: 'copy' | 'compress' | 'extract'; state: string; phase: string;
 sources: Array<{ path: string; version: string }>; destination: string; name?: string;
 target: { mode: string; directory?: string; name?: string };
 bytes: number; items: number; published: string[]; intent?: string; code?: string; cancel_requested: boolean;
}
export interface JobRequest {
 kind: FileJob['kind']; key: string; previous?: string; path?: string; version?: string;
 destination?: string; name?: string; target?: FileJob['target'];
 listing_token?: string; entries?: Array<{ name: string; version: string }>;
}
export const api = {
 getJobs: () => request<{ ok: true; jobs: FileJob[] }>('api/jobs'),
 createJob: (body: JobRequest) => request<{ ok: true; job: FileJob }>(body.previous ? `api/jobs/${encodeURIComponent(body.previous)}/retry` : 'api/jobs', { method: 'POST', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
 cancelJob: (id: string) => request<{ ok: true; job: FileJob }>(`api/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST', headers: csrfHeaders() }),
  startSizeScan: (path: string, kind: 'directory' | 'trash' = 'directory') => request<{ ok: true; scan: SizeScan }>('api/size-scans', { method: 'POST', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ path, kind }) }),
  getSizeScan: (id: string) => request<{ ok: true; scan: SizeScan }>(`api/size-scans/${encodeURIComponent(id)}`, { headers: jsonHeaders }),
  cancelSizeScan: (id: string) => request<{ ok: true; scan: SizeScan }>(`api/size-scans/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { ...jsonHeaders, ...csrfHeaders() } }),
  prepareUploadDirectories: (path: string, directories: string[]) => request<{ ok: true; created: string[] }>('api/uploads/directories', { method: 'POST', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ path, directories }) }),
  getFavorites: () => request<{ ok: true; entries: Favorite[] }>('api/favorites', { headers: jsonHeaders }),
  addFavorite: (path: string, label: string) => request<{ ok: true; entry: Favorite }>('api/favorites', { method: 'POST', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ path, label }) }),
  renameFavorite: (entry: Favorite, label: string) => request<{ ok: true; entry: Favorite }>(`api/favorites/${encodeURIComponent(entry.id)}`, { method: 'PATCH', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ label, revision: entry.revision }) }),
  removeFavorite: (entry: Favorite) => request<{ ok: true; entry: Favorite }>(`api/favorites/${encodeURIComponent(entry.id)}`, { method: 'DELETE', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ revision: entry.revision }) }),
  search: (parameters: Record<string, string>, signal?: AbortSignal) => request<{ ok: true; result: SearchResult }>(`api/search?${new URLSearchParams(parameters)}`, { headers: jsonHeaders, signal }),
  previewArchive: (path: string, version: string, signal?: AbortSignal) => request<{ ok: true; preview: ArchivePreview }>(`api/archive/preview?${new URLSearchParams({ path, version })}`, { headers: jsonHeaders, signal }),
  login: async (username: string, password: string, remember = false) => normalizeSession(await request<SessionBootstrapResponse>('api/session/login', {
    method: 'POST', headers: { ...jsonHeaders, 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password, remember })
  })),
  getSession: async () => normalizeSession(await request<SessionBootstrapResponse>('api/session', { headers: jsonHeaders })),
  logout: () => request<{ ok: true }>('api/session/logout', { method: 'POST', headers: { ...jsonHeaders, ...csrfHeaders() } }),
  getListing: async (path = '') => normalizeListing(await request<ListingResponse>(`api/listing?path=${encodeURIComponent(path)}`, { headers: jsonHeaders })),
  getDirectories: (path = '') => request<{ ok: true; path: string; dirs: Directory[] }>(`api/directories?path=${encodeURIComponent(path)}`, { headers: jsonHeaders }),
  getProperties: (path: string) => request<{ ok: true; properties: Properties }>(`api/properties?basic=true&path=${encodeURIComponent(path)}`, { headers: jsonHeaders }),
  getTrash: async (cursor?: string) => normalizeTrash(await request<TrashResponse>(`api/trash${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`, { headers: jsonHeaders })),
  restoreTrash: (id: string) => request<{ ok: true; path: string }>(`api/trash/${encodeURIComponent(id)}/restore`, { method: 'POST', headers: { ...csrfHeaders(), ...jsonHeaders } }),
  purgeTrash: (id: string, confirmation: string) => request<{ ok: true }>(`api/trash/${encodeURIComponent(id)}/purge`, { method: 'POST', headers: { ...csrfHeaders(), 'Content-Type': 'application/json', ...jsonHeaders }, body: JSON.stringify({ confirmation }) }),
  emptyTrash: (confirmation: string) => request<{ ok: true; affected: number }>('api/trash/empty', { method: 'POST', headers: { ...csrfHeaders(), 'Content-Type': 'application/json', ...jsonHeaders }, body: JSON.stringify({ confirmation }) }),
  getEditorContent: (path: string) => request<{ ok: true; editor: EditorDocument }>(`api/editor/content?path=${encodeURIComponent(path)}`, { headers: jsonHeaders }),
  saveEditorContent: (path: string, content: string, expectedVersion: string) => request<{ ok: true; editor: EditorDocument }>('api/editor/content', {
    method: 'PUT', headers: { ...jsonHeaders, ...csrfHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify({ path, content, expected_version: expectedVersion })
  }),
  mutate: <T>(endpoint: string, values: Record<string, string>) => request<T>(endpoint, {
    method: 'POST', headers: { ...csrfHeaders(), 'Content-Type': 'application/x-www-form-urlencoded', ...jsonHeaders }, body: new URLSearchParams(values)
  }),
  batch: <T>(endpoint: string, body: unknown) => request<T>(endpoint, {
    method: 'POST', headers: { ...csrfHeaders(), 'Content-Type': 'application/json', ...jsonHeaders }, body: JSON.stringify(body)
  }),
  createUpload: (metadata: { path: string; name: string; size: number; sha256?: string }, id: string, token: string) => request<{ ok: true; upload: UploadStatus }>('api/uploads', {
    method: 'POST', headers: { ...csrfHeaders(), 'Content-Type': 'application/json', 'X-Upload-ID': id, 'X-Upload-Token': token, ...jsonHeaders }, body: JSON.stringify(metadata)
  }),
  getUpload: (id: string, token: string) => request<{ ok: true; upload: UploadStatus }>(`api/uploads/${encodeURIComponent(id)}`, { headers: { 'X-Upload-Token': token, ...jsonHeaders } }),
  completeUpload: (id: string, token: string) => request<{ ok: true; repeated: boolean; upload: UploadStatus }>(`api/uploads/${encodeURIComponent(id)}/complete`, {
    method: 'POST', headers: { ...csrfHeaders(), 'X-Upload-Token': token, ...jsonHeaders }
  }),
  cancelUpload: (id: string, token: string) => request<{ ok: true }>(`api/uploads/${encodeURIComponent(id)}`, { method: 'DELETE', headers: { ...csrfHeaders(), 'X-Upload-Token': token, ...jsonHeaders } })
};

export function xhrUploadPart(input: { id: string; token: string; index: number; bytes: Blob; sha256: string; signal?: AbortSignal; onProgress?: (loaded: number, total: number) => void }): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      input.signal?.removeEventListener('abort', abort);
      callback();
    };
    const abort = () => { xhr.abort(); finish(() => reject(new ApiError(0, 'aborted'))); };
    if (input.signal?.aborted) { abort(); return; }
    input.signal?.addEventListener('abort', abort, { once: true });
    xhr.open('PUT', baseUrl(`api/uploads/${encodeURIComponent(input.id)}/parts/${input.index}`));
    xhr.withCredentials = true;
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.setRequestHeader('X-Upload-ID', input.id);
    xhr.setRequestHeader('X-Upload-Token', input.token);
    xhr.setRequestHeader('X-Upload-Part-SHA256', input.sha256);
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken);
    xhr.upload.onprogress = (event) => { if (event.lengthComputable) input.onProgress?.(event.loaded, event.total); };
    xhr.onerror = () => finish(() => reject(new ApiError(xhr.status || 0, 'network_error')));
    xhr.onabort = () => finish(() => reject(new ApiError(0, 'aborted')));
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) finish(resolve);
      else {
        try { finish(() => reject(new ApiError(xhr.status, (JSON.parse(xhr.responseText) as ApiErrorBody).code))); }
        catch { finish(() => reject(new ApiError(xhr.status, `http_${xhr.status}`))); }
      }
    };
    xhr.send(input.bytes);
  });
}
