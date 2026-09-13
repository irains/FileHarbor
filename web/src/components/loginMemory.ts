import { getRuntime } from '../runtime';

interface SavedLogin { version: 1; username: string; password: string }
const key = () => `fileharbor.login.v1:${getRuntime().basePath || '/'}`;

export function readLoginMemory(): { saved?: SavedLogin; failed?: boolean } {
  try {
    const raw = localStorage.getItem(key());
    if (!raw) return {};
    const saved: unknown = JSON.parse(raw);
    if (typeof saved !== 'object' || saved === null || !('version' in saved) || saved.version !== 1
      || !('username' in saved) || typeof saved.username !== 'string' || !saved.username
      || !('password' in saved) || typeof saved.password !== 'string' || !saved.password) return { failed: true };
    return { saved: saved as SavedLogin };
  } catch { return { failed: true }; }
}

export function saveLoginMemory(username: string, password: string): boolean {
  try { localStorage.setItem(key(), JSON.stringify({ version: 1, username, password })); return true; }
  catch { return false; }
}

export function clearLoginMemory(): boolean {
  try { localStorage.removeItem(key()); return true; }
  catch { return false; }
}
