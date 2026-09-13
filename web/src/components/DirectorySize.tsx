import { useState } from 'react';
import { Alert, Button, LinearProgress, Stack, Typography } from '@mui/material';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError } from '../api/client';
import { useI18n } from '../i18n';
import { formatBytes } from '../formatBytes';

export function DirectorySize({ path, kind = 'directory' }: { path: string; kind?: 'directory' | 'trash' }) {
  const { t } = useI18n();
  const client = useQueryClient();
  const [id, setID] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const query = useQuery({ queryKey: ['size-scan', id], queryFn: () => api.getSizeScan(id!), enabled: Boolean(id), refetchInterval: query => query.state.data?.scan.state === 'running' ? 1000 : false });
  const start = async () => {
    setPending(true); setError(null);
    try { const response = await (kind === 'trash' ? api.startSizeScan(path, kind) : api.startSizeScan(path)); client.setQueryData(['size-scan', response.scan.id], response); setID(response.scan.id); }
    catch (failure) { setError(failure instanceof ApiError ? failure.code : 'generic'); }
    finally { setPending(false); }
  };
  const scan = query.data?.scan;
  return <Stack spacing={1} sx={{ py: 2 }}>
    <Typography variant="caption">{t('sizeScan.hint')}</Typography>
    <Button disabled={pending || scan?.state === 'running'} onClick={() => void start()}>{t(kind === 'trash' ? 'sizeScan.calculateTrash' : 'sizeScan.calculate')}</Button>
    {(pending || scan?.state === 'running') && <LinearProgress />}
    {scan?.state === 'running' && <Button onClick={() => void api.cancelSizeScan(scan.id).then(() => query.refetch()).catch(() => setError('generic'))}>{t('action.cancel')}</Button>}
    {(error || query.isError || scan?.code) && <Alert severity="warning">{t(`error.${error || scan?.code || 'generic'}`)}</Alert>}
    {scan?.result && <>
      <Typography>{formatBytes(scan.result.bytes)}</Typography>
      <Typography variant="caption">{t('sizeScan.count', { files: scan.result.files, directories: scan.result.directories })}</Typography>
      <Typography variant="caption">{new Date(scan.result.scanned_at).toLocaleString()}</Typography>
      {scan.result.incomplete && <Alert severity="warning">{t('properties.incomplete')}</Alert>}
    </>}
  </Stack>;
}
