import { useState } from 'react';
import { TaskAlt } from '@mui/icons-material';
import { Alert, Button, LinearProgress, Stack, Typography } from '@mui/material';
import { Link } from 'react-router-dom';
import { api, ApiError, type FileJob } from '../api/client';
import { SidePanel } from '../components/SidePanel';
import { useI18n } from '../i18n';
import { formatBytes } from '../formatBytes';
import { directoryRoute } from '../runtime';

export function TaskPanel({ open, onClose, jobs, loading, failed, refresh, retry, canMutate }: { canMutate: boolean; open: boolean; onClose: () => void; jobs: FileJob[]; loading: boolean; failed: boolean; refresh: () => Promise<unknown>; retry: (job: FileJob) => Promise<void> }) {
  const { t } = useI18n();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<string | null>(null);
  const act = async (operation: () => Promise<unknown>) => {
    setBusy(true); setError('');
    try { await operation(); await refresh(); setConfirm(null); }
    catch (failure) { setError(failure instanceof ApiError ? failure.code : 'generic'); }
    finally { setBusy(false); }
  };
  return <SidePanel open={open} onClose={onClose} icon={<TaskAlt />} title={t('tasks.title')}>
    <Stack spacing={2} sx={{ '& .MuiButton-root': { minHeight: 44 }, overflowWrap: 'anywhere' }}>
      <Typography variant="caption">{t('tasks.hint')}</Typography>
      {loading && <LinearProgress />}
      {(failed || error) && <Alert severity="error" action={<Button onClick={() => void refresh()}>{t('action.retry')}</Button>}>{t(`error.${error || 'generic'}`)}</Alert>}
      {!loading && !jobs.length && <Typography>{t('tasks.empty')}</Typography>}
      {jobs.map(job => <Stack key={job.id} spacing={1} sx={{ py: 2, borderBottom: '1px solid', borderColor: 'divider' }}>
        <Typography sx={{ overflowWrap: 'anywhere' }}>{job.sources.map(source => source.path).join(', ')}</Typography>
        <Typography variant="caption">{t('dialog.destination')}: {(job.kind === 'extract' ? job.target.mode === 'chosen' ? job.target.directory : [job.sources[0].path.split('/').slice(0, -1).join('/'), job.target.mode === 'new_folder' ? job.target.name : ''].filter(Boolean).join('/') : job.destination) || t('workspace.root')}</Typography>
        <Typography variant="body2">{t(`tasks.${job.kind}`)} · {t(`tasks.${job.state}`)}</Typography>
        <Typography variant="caption">{t(`tasks.${job.phase}`)} · {formatBytes(job.bytes)} · {t('tasks.items', { count: job.items })}</Typography>
        {['queued', 'running'].includes(job.state) && <><LinearProgress /><Button disabled={!canMutate || busy || job.cancel_requested} onClick={() => void act(() => api.cancelJob(job.id))}>{t(job.cancel_requested ? 'tasks.cancelling' : 'action.cancel')}</Button></>}
        {job.code && <Alert severity="warning">{t(`error.${job.code}`)}</Alert>}
        {job.intent && <Alert severity="warning">{t('tasks.uncertain')} {job.intent}</Alert>}
        {job.published.map(output => <Typography key={output} variant="caption" sx={{ overflowWrap: 'anywhere' }}>{t('tasks.published')}: {output}</Typography>)}
        {job.published.length > 0 && <Button component={Link} to={directoryRoute(job.kind === 'extract' && job.target.mode === 'new_folder' ? job.published[0] : job.published[0].split('/').slice(0, -1).join('/'))} onClick={onClose}>{t('tasks.open')}</Button>}
        {canMutate && ['failed', 'cancelled', 'partial', 'interrupted'].includes(job.state) && (confirm === job.id ? <><Alert severity="warning">{t('tasks.retryHint')}</Alert><Button autoFocus disabled={busy} onClick={() => void act(() => retry(job))}>{t('action.confirm')}</Button><Button onClick={() => setConfirm(null)}>{t('action.cancel')}</Button></> : <Button disabled={busy} onClick={() => setConfirm(job.id)}>{t('action.retry')}</Button>)}
      </Stack>)}
    </Stack>
  </SidePanel>;
}
