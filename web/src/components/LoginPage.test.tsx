import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { I18nProvider } from '../i18n';
import { SessionProvider } from '../session/SessionProvider';
import { LoginPage } from './LoginPage';

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
  });
});
