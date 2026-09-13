import { useRef, useState } from 'react';
import { ArchiveOutlined, ContentCopyOutlined, DeleteOutline, FolderOpenOutlined, Inventory2Outlined, Refresh, TaskAlt } from '@mui/icons-material';
import { Alert, Box, Button, Chip, IconButton, LinearProgress, Stack, Tooltip, Typography } from '@mui/material';
import { Link } from 'react-router-dom';
import { api, ApiError, type FileJob } from '../api/client';
import { SidePanel } from '../components/SidePanel';
import { EmptyState } from '../components/EmptyState';
import { useI18n } from '../i18n';
import { formatBytes } from '../formatBytes';
import { directoryRoute } from '../runtime';

function destination(job: FileJob) {
  if (job.kind !== 'extract') return job.destination;
  if (job.target.mode === 'chosen') return job.target.directory;
  const parent = job.sources[0]?.path.split('/').slice(0, -1).join('/') || '';
  return [parent, job.target.mode === 'new_folder' ? job.target.name : ''].filter(Boolean).join('/');
}

export function TaskPanel({ open, onClose, jobs, loading, failed, refresh, retry, remove, canMutate }: {
  canMutate: boolean; open: boolean; onClose: () => void; jobs: FileJob[]; loading: boolean; failed: boolean;
  refresh: () => Promise<unknown>; retry: (job: FileJob) => Promise<void>; remove?: (job: FileJob) => Promise<void>;
}) {
  const { t } = useI18n();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ id: string; action: 'retry' | 'remove' } | null>(null);
  const list = useRef<HTMLDivElement>(null);
  const act = async (id: string, operation: () => Promise<unknown>, removed = false) => {
    setBusy(id); setError('');
    try {
      await operation(); await refresh(); setConfirm(null);
      if (removed) list.current?.focus();
    } catch (failure) { setError(failure instanceof ApiError ? failure.code : 'generic'); }
    finally { setBusy(null); }
  };
  const active = jobs.filter(job => ['queued', 'running'].includes(job.state));
  const finished = jobs.filter(job => !['queued', 'running'].includes(job.state));
  return <SidePanel open={open} onClose={onClose} icon={<TaskAlt />} title={t('tasks.title')} width={{ sm: 480 }}
    trailing={<Tooltip title={t('workspace.refresh')}><IconButton aria-label={t('workspace.refresh')} onClick={() => void refresh()}><Refresh /></IconButton></Tooltip>}>
    <Stack spacing={2} sx={{ '& .MuiButton-root': { minHeight: 44 }, overflowWrap: 'anywhere', minWidth: 0 }}>
      {loading && <LinearProgress aria-label={t('tasks.title')} />}
      {(failed || error) && <Alert severity="error">{t(`error.${error || 'job_unavailable'}`)}</Alert>}
      {!loading && !failed && !jobs.length && <EmptyState icon={<TaskAlt />} title={t('tasks.empty')} caption={t('tasks.emptyHint')} />}
      <Box ref={list} tabIndex={-1} aria-label={t('tasks.title')} sx={{ outline: 'none', minWidth: 0 }}>
        {([{ key: 'active', entries: active }, { key: 'history', entries: finished }] as const).map(group => group.entries.length > 0 && <Box key={group.key} sx={{ mb: 2 }}>
          <Stack direction="row" alignItems="center" spacing={1} sx={{ pb: 1, borderBottom: '1px solid', borderColor: 'divider' }}>
            <Typography variant="overline" color="text.secondary">{t(`tasks.${group.key}`)}</Typography>
            <Typography variant="caption" color="text.secondary">{group.entries.length}</Typography>
          </Stack>
          {group.entries.map(job => {
            const running = ['queued', 'running'].includes(job.state);
            const warning = ['failed', 'partial', 'interrupted'].includes(job.state);
            const title = job.sources[0]?.path.split('/').pop() || t(`tasks.${job.kind}`);
            const icon = job.kind === 'copy' ? <ContentCopyOutlined /> : job.kind === 'compress' ? <ArchiveOutlined /> : <Inventory2Outlined />;
            const confirmation = confirm?.id === job.id ? confirm.action : null;
            return <Stack component="article" aria-label={title} key={job.id} spacing={1.5} sx={{ py: 2.5, borderBottom: '1px solid', borderColor: 'divider' }}>
              <Stack direction="row" spacing={1.25} alignItems="flex-start">
                <Box sx={{ display: 'flex', p: 1, borderRadius: 1.5, bgcolor: 'action.hover', color: running ? 'primary.main' : 'text.secondary', flexShrink: 0 }}>{icon}</Box>
                <Box sx={{ flex: 1, minWidth: 0 }}>
                  <Typography variant="bodyStrong" component="h3" sx={{ m: 0 }}>{title}{job.sources.length > 1 && <Typography component="span" variant="caption" color="text.secondary"> +{job.sources.length - 1}</Typography>}</Typography>
                  <Typography variant="caption" color="text.secondary">{t(`tasks.${job.kind}`)}</Typography>
                </Box>
                <Chip size="small" variant="outlined" color={warning ? 'warning' : job.state === 'succeeded' ? 'success' : running ? 'primary' : 'default'} label={t(`tasks.${job.state}`)} sx={{ height: 'auto', minHeight: 24, maxWidth: '46%', '& .MuiChip-label': { whiteSpace: 'normal', py: .25 } }} />
              </Stack>
              <Box sx={{ minWidth: 0 }}>
                <Typography variant="caption" color="text.secondary" component="div">{t('dialog.destination')}</Typography>
                <Typography variant="body2">{destination(job) || t('workspace.root')}</Typography>
              </Box>
              <Stack spacing={.75}>
                <Stack direction="row" flexWrap="wrap" justifyContent="space-between" gap={.5}>
                  <Typography variant="caption" color="text.secondary">{t(`tasks.${job.phase}`)}</Typography>
                  <Typography variant="caption" color="text.secondary" sx={{ fontVariantNumeric: 'tabular-nums' }}>{formatBytes(job.bytes)} · {t('tasks.items', { count: job.items })}</Typography>
                </Stack>
                {running && <LinearProgress aria-label={t(`tasks.${job.state}`)} sx={{ height: 3, borderRadius: 2 }} />}
              </Stack>
              {job.code && <Alert severity="warning">{t(`error.${job.code}`)}</Alert>}
              {job.intent && <Alert severity="warning">{t('tasks.uncertain')} {job.intent}</Alert>}
              <Box component="details" sx={{ '& summary': { cursor: 'pointer', color: 'text.secondary', fontSize: '.8125rem', minHeight: 44, display: 'list-item', alignContent: 'center' } }}>
                <Box component="summary">{t('tasks.details')}</Box>
                <Stack spacing={1} sx={{ pt: 1 }}>
                  <Typography variant="caption" color="text.secondary">{t('tasks.sources')}</Typography>
                  {job.sources.map(source => <Typography key={source.path} variant="body2">{source.path}</Typography>)}
                  {job.published.length > 0 && <Typography variant="caption" color="text.secondary">{t('tasks.published')}</Typography>}
                  {job.published.map(output => <Typography key={output} variant="body2">{output}</Typography>)}
                </Stack>
              </Box>
              {confirmation ? <Stack spacing={1}>
                <Alert severity={confirmation === 'retry' ? 'warning' : 'info'}>{t(confirmation === 'retry' ? 'tasks.retryHint' : 'tasks.removeHint')}</Alert>
                <Stack direction="row" gap={1}>
                  <Button autoFocus variant="contained" disabled={busy !== null} onClick={() => void act(job.id, () => confirmation === 'retry' ? retry(job) : remove!(job), confirmation === 'remove')}>{t('action.confirm')}</Button>
                  <Button disabled={busy !== null} onClick={() => setConfirm(null)}>{t('action.cancel')}</Button>
                </Stack>
              </Stack> : <Stack direction="row" flexWrap="wrap" gap={.75}>
                {running && <Button size="small" variant="outlined" disabled={!canMutate || busy !== null || job.cancel_requested} onClick={() => void act(job.id, () => api.cancelJob(job.id))}>{t(job.cancel_requested ? 'tasks.cancelling' : 'action.cancel')}</Button>}
                {job.published.length > 0 && <Button size="small" startIcon={<FolderOpenOutlined />} component={Link} to={directoryRoute(job.kind === 'extract' && job.target.mode === 'new_folder' ? job.published[0] : job.published[0].split('/').slice(0, -1).join('/'))} onClick={onClose}>{t('tasks.open')}</Button>}
                {canMutate && ['failed', 'cancelled', 'partial', 'interrupted'].includes(job.state) && <Button size="small" variant="outlined" disabled={busy !== null} onClick={() => setConfirm({ id: job.id, action: 'retry' })}>{t('action.retry')}</Button>}
                {canMutate && !running && remove && <Button size="small" color="inherit" startIcon={<DeleteOutline />} sx={{ color: 'text.secondary', ml: 'auto' }} disabled={busy !== null} onClick={() => setConfirm({ id: job.id, action: 'remove' })}>{t('tasks.remove')}</Button>}
              </Stack>}
            </Stack>;
          })}
        </Box>)}
      </Box>
      {jobs.length > 0 && <Typography variant="caption" color="text.secondary">{t('tasks.hint')}</Typography>}
    </Stack>
  </SidePanel>;
}
