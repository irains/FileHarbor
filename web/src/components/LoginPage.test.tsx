import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { api, type BrowserSession } from '../api/client';
import { I18nProvider } from '../i18n';
import { SessionProvider } from '../session/SessionProvider';
import { LoginPage } from './LoginPage';

const session: BrowserSession = {
  username: 'alice', csrfToken: 'csrf', expiresAt: '', basePath: '', language: 'en',
  capabilities: { browse: true, upload: true, mutate: true, editorSave: true }
};
const assign = vi.fn();
const store = vi.fn();
const get = vi.fn();
const credentialConstructor = vi.fn(function (this: { id: string; password: string }, data: { id: string; password: string }) {
  Object.assign(this, data);
});

function renderLogin() {
  return render(<I18nProvider><SessionProvider loginPage><LoginPage /></SessionProvider></I18nProvider>);
}

function fillAndSubmit(container: HTMLElement, rememberPassword = true, remember = false) {
  fireEvent.change(container.querySelector('input[name="username"]')!, { target: { value: '  alice  ' } });
  fireEvent.change(container.querySelector('input[name="password"]')!, { target: { value: 'private-password' } });
  if (rememberPassword) fireEvent.click(container.querySelector('input[name="rememberPassword"]')!);
  if (remember) fireEvent.click(container.querySelector('input[name="remember"]')!);
  fireEvent.submit(container.querySelector('form')!);
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  store.mockResolvedValue(null);
  vi.stubGlobal('isSecureContext', true);
  vi.stubGlobal('PasswordCredential', credentialConstructor);
  vi.stubGlobal('navigator', { credentials: { store, get } });
  const originalWindow = window;
  vi.stubGlobal('window', new Proxy(originalWindow, {
    get(target, key) {
      if (key === 'location') return { ...target.location, assign };
      return Reflect.get(target, key);
    }
  }));
  vi.spyOn(api, 'login').mockResolvedValue(session);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('LoginPage', () => {
  it('keeps outlined labels shrunk and stable from the first focused render', () => {
    const { container } = render(<I18nProvider><SessionProvider loginPage><LoginPage /></SessionProvider></I18nProvider>);

    for (const name of ['username', 'password']) {
      const input = container.querySelector<HTMLInputElement>(`input[name="${name}"]`);
      const label = input && container.querySelector(`label[for="${input.id}"]`);
      expect(label).toHaveClass('MuiInputLabel-shrink');
      expect(label).not.toHaveClass('MuiInputLabel-animated');
    }

    expect(container.querySelector('input[name="username"]')).toHaveFocus();
    expect(container.querySelector('input[name="username"]')).toHaveAttribute('autocomplete', 'username');
    expect(container.querySelector('input[name="password"]')).toHaveAttribute('autocomplete', 'current-password');
    expect(container.querySelector('input[name="password"]')).toHaveAttribute('type', 'password');
    expect(container.querySelector('form')).toHaveAttribute('autocomplete', 'on');
    expect(screen.getByRole('checkbox', { name: 'Keep me signed in for 30 days' })).not.toBeChecked();
    expect(container.querySelector('input[name="rememberPassword"]')).not.toBeChecked();
    expect(get).not.toHaveBeenCalled();
  });
});


describe('local login memory', () => {
  const key = 'fileharbor.login.v1:/';
  it('saves after authentication and fills the next visit without signing in', async () => {
    const view = renderLogin();
    fillAndSubmit(view.container);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/'));
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({ version: 1, username: 'alice', password: 'private-password' });
    view.unmount();
    vi.mocked(api.login).mockClear();
    const next = renderLogin();
    expect(next.container.querySelector('input[name="username"]')).toHaveValue('alice');
    expect(next.container.querySelector('input[name="password"]')).toHaveValue('private-password');
    expect(next.container.querySelector('input[name="rememberPassword"]')).toBeChecked();
    expect(api.login).not.toHaveBeenCalled();
    fireEvent.click(next.container.querySelector('input[name="rememberPassword"]')!);
    expect(localStorage.getItem(key)).toBeNull();
    expect(store).not.toHaveBeenCalled();
  });
  it.each([false, true])('does not save without opt-in (keep signed in %s)', async remember => {
    const view = renderLogin();
    fillAndSubmit(view.container, false, remember);
    await waitFor(() => expect(assign).toHaveBeenCalled());
    expect(api.login).toHaveBeenCalledWith('alice', 'private-password', remember);
    expect(localStorage.getItem(key)).toBeNull();
  });
  it('waits for authentication before saving', async () => {
    let complete!: (value: BrowserSession) => void;
    vi.mocked(api.login).mockReturnValue(new Promise(resolve => { complete = resolve; }));
    const view = renderLogin(); fillAndSubmit(view.container);
    await waitFor(() => expect(api.login).toHaveBeenCalled());
    expect(localStorage.getItem(key)).toBeNull();
    complete(session);
    await waitFor(() => expect(assign).toHaveBeenCalled());
    expect(localStorage.getItem(key)).not.toBeNull();
  });
  it('preserves a saved record when changed credentials fail authentication', async () => {
    const original = JSON.stringify({ version: 1, username: 'old-user', password: 'old-password' });
    localStorage.setItem(key, original);
    vi.mocked(api.login).mockRejectedValue(new Error('Failed'));
    const view = renderLogin(); fillAndSubmit(view.container, false);
    await screen.findByRole('alert');
    expect(localStorage.getItem(key)).toBe(original);
  });
  it('does not save failed authentication', async () => {
    vi.mocked(api.login).mockRejectedValue(new Error('Failed'));
    const view = renderLogin(); fillAndSubmit(view.container);
    await screen.findByRole('alert');
    expect(localStorage.getItem(key)).toBeNull();
    expect(assign).not.toHaveBeenCalled();
  });
  it('reports save failure but lets the authenticated user continue', async () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Blocked'); });
    const view = renderLogin(); fillAndSubmit(view.container);
    await screen.findByText('Signed in, but this browser could not save your login.');
    expect(assign).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Continue to workspace' }));
    expect(assign).toHaveBeenCalledWith('/');
  });
  it('reports removal failure', () => {
    localStorage.setItem(key, JSON.stringify({ version: 1, username: 'alice', password: 'private-password' }));
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('Blocked'); });
    const view = renderLogin();
    fireEvent.click(view.container.querySelector('input[name="rememberPassword"]')!);
    expect(screen.getByRole('alert')).toHaveTextContent('Saved login could not be removed');
  });
  it('handles malformed stored data without filling it', () => {
    localStorage.setItem(key, '{');
    const view = renderLogin();
    expect(view.container.querySelector('input[name="password"]')).toHaveValue('');
    expect(screen.getByRole('alert')).toHaveTextContent('Saved login could not be read');
  });
});
