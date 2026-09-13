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
import { readLoginMemory, saveLoginMemory, clearLoginMemory } from './loginMemory';

const loginSchema = z.object({ username: z.string().trim().min(1), password: z.string().min(1), remember: z.boolean(), rememberPassword: z.boolean() });
type LoginValues = z.infer<typeof loginSchema>;
const loginInputLabelProps = { shrink: true, disableAnimation: true } as const;

export function LoginPage() {
  const { t } = useI18n();
  const { setSession } = useSession();
  const [memory] = useState(readLoginMemory);
  const [memoryError, setMemoryError] = useState<string | null>(memory.failed ? 'login.memoryReadFailed' : null);
  const [authenticated, setAuthenticated] = useState(false);
  const [serverError, setServerError] = useState<string | null>(null);
  const { control, handleSubmit, formState: { isSubmitting, errors } } = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { username: memory.saved?.username ?? '', password: memory.saved?.password ?? '', remember: false, rememberPassword: Boolean(memory.saved) },
    mode: 'onBlur',
    reValidateMode: 'onChange'
  });
  const onSubmit = async (values: LoginValues) => {
    if (authenticated) return;
    setServerError(null);
    try {
      const session = await api.login(values.username, values.password, values.remember);
      setSession(session);
      setAuthenticated(true);
      const stored = values.rememberPassword ? saveLoginMemory(values.username, values.password) : clearLoginMemory();
      if (!stored) { setMemoryError(values.rememberPassword ? 'login.memorySaveFailed' : 'login.memoryClearFailed'); return; }
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
            {memoryError && <Alert severity="warning">{t(memoryError)}</Alert>}
            <Controller name="username" control={control} render={({ field }) => <TextField {...field} autoComplete="username" autoFocus label={t('login.username')} slotProps={{ inputLabel: loginInputLabelProps }} error={Boolean(errors.username)} helperText={errors.username ? t('login.usernameRequired') : undefined} fullWidth required />} />
            <Controller name="password" control={control} render={({ field }) => <TextField {...field} type="password" autoComplete="current-password" label={t('login.password')} slotProps={{ inputLabel: loginInputLabelProps }} error={Boolean(errors.password)} helperText={errors.password ? t('login.passwordRequired') : undefined} fullWidth required />} />
            <Stack spacing={0.5}>
              <Controller name="remember" control={control} render={({ field: { value, ...field } }) => <FormControlLabel control={<Checkbox {...field} checked={value} />} label={t('login.remember')} />} />
              <Typography variant="caption" color="text.secondary">{t('login.rememberHint')}</Typography>
            </Stack>
            <Stack spacing={0.5}>
              <Controller name="rememberPassword" control={control} render={({ field: { value, ...field } }) => <FormControlLabel control={<Checkbox {...field} checked={value} disabled={isSubmitting || authenticated} onChange={(event) => { field.onChange(event); if (!event.target.checked) setMemoryError(clearLoginMemory() ? null : 'login.memoryClearFailed'); }} slotProps={{ input: { 'aria-describedby': 'login-memory-hint' } }} />} label={t('login.rememberPassword')} />} />
              <Typography id="login-memory-hint" variant="caption" color="text.secondary">{t('login.rememberPasswordHint')}</Typography>
            </Stack>
            {authenticated ? <Button type="button" size="large" variant="contained" onClick={() => window.location.assign(getRuntime().loginNext)}>{t('login.continue')}</Button> : <Button type="submit" size="large" variant="contained" disabled={isSubmitting}>{t('login.signIn')}</Button>}
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
