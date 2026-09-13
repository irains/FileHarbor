import { openDB, type DBSchema } from 'idb';

// Version 1 deliberately stores only durable capability metadata. File bytes,
// CSRF/session data, transfer progress, and transient server state stay memory-only.
export interface StoredUpload {
  version: 1;
  id: string;
  token: string;
  scope: string;
  path: string;
  name: string;
  relativePath?: string;
  size: number;
  lastModified: number;
  sha256: string;
  createdAt: number;
}

interface UploadSchema extends DBSchema {
  uploads: { key: string; value: StoredUpload; indexes: { 'by-scope': string } };
}

export const UPLOAD_DB_NAME = 'fileharbor-reliable-uploads';

export const uploadDb = () => openDB<UploadSchema>(UPLOAD_DB_NAME, 1, {
  upgrade(db) {
    const store = db.createObjectStore('uploads', { keyPath: 'id' });
    store.createIndex('by-scope', 'scope');
  }
});

export const uploadScope = (origin: string, basePath: string, username: string) => `${origin}${basePath}|${username}`;
export async function listStoredUploads(scope: string) { return (await uploadDb()).getAllFromIndex('uploads', 'by-scope', scope); }
export async function putStoredUpload(upload: StoredUpload) { return (await uploadDb()).put('uploads', upload); }
export async function deleteStoredUpload(id: string) { return (await uploadDb()).delete('uploads', id); }
