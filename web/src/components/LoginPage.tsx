import { useState } from 'react';
import { Alert, Box, Button, Card, CardContent, Checkbox, FormControlLabel, Stack, TextField, Typography } from '@mui/material';
import { Controller, useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { ApiError, api } from '../api/client';
import { getRuntime, routeUrl } from '../runtime';
import { useI18n } from '../i18n';
import { useSession } from '../session/SessionProvider';
import { Mark } from './Mark';
import { surface } from '../tokens';

const loginSchema = z.object({ username: z.string().trim().min(1), password: z.string().min(1), remember: z.boolean(), rememberPassword: z.boolean() });
type LoginValues = z.infer<typeof loginSchema>;
const loginInputLabelProps = { shrink: true, disableAnimation: true } as const;

// PasswordCredential is not declared by every TypeScript DOM library version.
type PasswordCredentialConstructor = new (data: { id: string; password: string }) => Credential;

async function requestBrowserPasswordSave(username: string, password: string): Promise<void> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    const PasswordCredential = (window as Window & { PasswordCredential?: PasswordCredentialConstructor }).PasswordCredential;
    if (!window.isSecureContext || !PasswordCredential || typeof navigator.credentials?.store !== 'function') return;
    // Only the browser's password manager receives the password. A fulfilled
    // request is not proof the browser saved it, so do not report success.
    const saving = navigator.credentials.store(new PasswordCredential({ id: username, password }));
    // Browser UI can leave this pending indefinitely; login must still finish.
    await Promise.race([
      saving,
      new Promise<void>(resolve => { timeout = setTimeout(resolve, 1000); })
    ]);
  } catch {
    // A denied/unavailable password manager must never turn a valid login into an error.
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

export function LoginPage() {
  const { t } = useI18n();
  const { setSession } = useSession();
  const [serverError, setServerError] = useState<string | null>(null);
  const { control, handleSubmit, formState: { isSubmitting, errors } } = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { username: '', password: '', remember: false, rememberPassword: false },
    mode: 'onBlur',
    reValidateMode: 'onChange'
  });
  const onSubmit = async (values: LoginValues) => {
    setServerError(null);
    try {
      const session = await api.login(values.username, values.password, values.remember);
      if (values.rememberPassword) await requestBrowserPasswordSave(values.username, values.password);
      setSession(session);
      window.location.assign(getRuntime().loginNext);
    } catch (error) {
      setServerError(error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'));
    }
  };
  return (
    <Box component="main" sx={{ minHeight: '100dvh', display: 'grid', placeItems: 'center', p: { xs: 2, sm: 4 } }}>
      <Card sx={{ ...surface, width: 'min(100%, 440px)' }}>
        <CardContent sx={{ p: { xs: 3, sm: 5 } }}>
          <Stack spacing={3} component="form" autoComplete="on" onSubmit={handleSubmit(onSubmit)} noValidate>
            <Stack spacing={1.5}>
              <Box sx={{ lineHeight: 0 }}><Mark size={32} /></Box>
              <Typography component="h1" variant="title">{t('login.title')}</Typography>
              <Typography variant="caption" color="text.secondary">{t('login.subtitle')}</Typography>
            </Stack>
            {serverError && <Alert severity="error">{serverError}</Alert>}
            <Controller name="username" control={control} render={({ field }) => <TextField {...field} autoComplete="username" autoFocus label={t('login.username')} slotProps={{ inputLabel: loginInputLabelProps }} error={Boolean(errors.username)} helperText={errors.username ? t('login.usernameRequired') : undefined} fullWidth required />} />
            <Controller name="password" control={control} render={({ field }) => <TextField {...field} type="password" autoComplete="current-password" label={t('login.password')} slotProps={{ inputLabel: loginInputLabelProps }} error={Boolean(errors.password)} helperText={errors.password ? t('login.passwordRequired') : undefined} fullWidth required />} />
            <Stack spacing={0.5}>
              <Controller name="remember" control={control} render={({ field: { value, ...field } }) => <FormControlLabel control={<Checkbox {...field} checked={value} />} label={t('login.remember')} />} />
              <Typography variant="caption" color="text.secondary">{t('login.rememberHint')}</Typography>
            </Stack>
            <Stack spacing={0.5}>
              <Controller name="rememberPassword" control={control} render={({ field: { value, ...field } }) => <FormControlLabel control={<Checkbox {...field} checked={value} slotProps={{ input: { 'aria-describedby': 'password-manager-hint' } }} />} label={t('login.rememberPassword')} />} />
              <Typography id="password-manager-hint" variant="caption" color="text.secondary">{t('login.rememberPasswordHint')}</Typography>
            </Stack>
            <Button type="submit" size="large" variant="contained" disabled={isSubmitting}>{t('login.signIn')}</Button>
          </Stack>
        </CardContent>
      </Card>
    </Box>
  );
}

export function isLoginRoute() {
  const current = window.location.pathname.replace(/\/+$/, '') || '/';
  return current === routeUrl('login').replace(/\/+$/, '');
}
