import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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


describe('browser password saving', () => {
  it.each([false, true])('does not save without explicit opt-in (keep signed in: %s)', async (remember) => {
    const { container } = renderLogin();
    fillAndSubmit(container, false, remember);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/'));
    expect(api.login).toHaveBeenCalledWith('alice', 'private-password', remember);
    expect(store).not.toHaveBeenCalled();
    expect(credentialConstructor).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
  });

  it('requests saving only after successful authentication, independently of keep signed in', async () => {
    let completeLogin!: (session: BrowserSession) => void;
    vi.mocked(api.login).mockReturnValue(new Promise(resolve => { completeLogin = resolve; }));
    const localWrite = vi.spyOn(Storage.prototype, 'setItem');
    const { container } = renderLogin();
    fillAndSubmit(container);
    await waitFor(() => expect(api.login).toHaveBeenCalledWith('alice', 'private-password', false));
    expect(store).not.toHaveBeenCalled();
    expect(credentialConstructor).not.toHaveBeenCalled();
    completeLogin(session);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/'));
    expect(credentialConstructor).toHaveBeenCalledExactlyOnceWith({ id: 'alice', password: 'private-password' });
    expect(store).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ id: 'alice', password: 'private-password' }));
    expect(get).not.toHaveBeenCalled();
    expect(localWrite).not.toHaveBeenCalled();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('continues full-page login after one second when saving never settles', async () => {
    vi.useFakeTimers();
    store.mockReturnValue(new Promise(() => {}));
    const clearTimer = vi.spyOn(globalThis, 'clearTimeout');
    const { container } = renderLogin();
    await act(async () => { fillAndSubmit(container); });
    expect(store).toHaveBeenCalledOnce();
    expect(assign).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(999); });
    expect(assign).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(assign).toHaveBeenCalledExactlyOnceWith('/');
    expect(clearTimer).toHaveBeenCalled();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('never saves failed authentication', async () => {
    vi.mocked(api.login).mockRejectedValue(new Error('Login failed'));
    const { container } = renderLogin();
    fillAndSubmit(container);
    await screen.findByRole('alert');
    expect(store).not.toHaveBeenCalled();
    expect(credentialConstructor).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });

  it.each(['insecure', 'missing constructor', 'missing credentials', 'missing store', 'rejected', 'throws'])('keeps login working when password saving is %s', async (condition) => {
    if (condition === 'insecure') vi.stubGlobal('isSecureContext', false);
    if (condition === 'missing constructor') vi.stubGlobal('PasswordCredential', undefined);
    if (condition === 'missing credentials') vi.stubGlobal('navigator', {});
    if (condition === 'missing store') vi.stubGlobal('navigator', { credentials: {} });
    if (condition === 'rejected') store.mockRejectedValue(new Error('Permission denied'));
    if (condition === 'throws') credentialConstructor.mockImplementationOnce(() => { throw new Error('Unavailable'); });
    const { container } = renderLogin();
    fillAndSubmit(container);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/'));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(container.querySelector('input[name="password"]')).toHaveAttribute('autocomplete', 'current-password');
    if (condition !== 'rejected') expect(store).not.toHaveBeenCalled();
    expect(get).not.toHaveBeenCalled();
  });
});
