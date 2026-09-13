import { DeleteForeverOutlined, DeleteOutline, RestoreOutlined } from '@mui/icons-material';
import { Alert, Box, Button, Divider, List, ListItem, ListItemIcon, ListItemText, Stack, TextField, Typography } from '@mui/material';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { api, ApiError, type TrashEntry } from '../api/client';
import { formatBytes } from '../formatBytes';
import { useI18n } from '../i18n';
import { DialogShell } from './DialogShell';
import { EmptyState } from './EmptyState';
import { DirectorySize } from './DirectorySize';
import { SidePanel } from './SidePanel';

type PendingPurge = { type: 'entry'; entry: TrashEntry } | { type: 'empty' } | null;

function formatDeletedAt(value: string) {
  return value ? new Date(value).toLocaleString() : '—';
}

function parentPath(path: string) {
  const parts = path.split('/');
  parts.pop();
  return parts.join('/');
}

export function RecycleBinPanel({
  open,
  onClose,
  mutable,
  currentDirectory,
  onRestored,
  onNotice
}: {
  open: boolean;
  onClose: () => void;
  mutable: boolean;
  currentDirectory: string;
  onRestored: (directory: string) => Promise<void>;
  onNotice: (message: string, severity: 'success' | 'error') => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [cursor, setCursor] = useState<string | undefined>();
  const [entries, setEntries] = useState<TrashEntry[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [pendingPurge, setPendingPurge] = useState<PendingPurge>(null);
  const [confirmation, setConfirmation] = useState('');
  const trashQuery = useQuery({ queryKey: ['trash', cursor], queryFn: () => api.getTrash(cursor), enabled: open });

  useEffect(() => {
    if (!open) return;
    setCursor(undefined);
    setEntries([]);
    setNextCursor(undefined);
  }, [open]);
  useEffect(() => {
    if (!open || !trashQuery.data) return;
    setEntries((previous) => cursor ? [...previous, ...trashQuery.data.entries] : trashQuery.data.entries);
    setNextCursor(trashQuery.data.nextCursor);
  }, [cursor, open, trashQuery.data]);
  useEffect(() => {
    setConfirmation('');
  }, [pendingPurge]);

  const invalidateTrash = async () => {
    await queryClient.invalidateQueries({ queryKey: ['trash'] });
  };
  const restore = useMutation({
    mutationFn: (entry: TrashEntry) => api.restoreTrash(entry.id),
    onSuccess: async (result, entry) => {
      await invalidateTrash();
      if (parentPath(result.path) === currentDirectory) await onRestored(currentDirectory);
      onNotice(t('recycleBin.restored', { name: entry.name }), 'success');
    },
    onError: (error) => onNotice(error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), 'error')
  });
  const purge = useMutation({
    mutationFn: ({ id, confirmation }: { id: string; confirmation: string }) => api.purgeTrash(id, confirmation),
    onSuccess: async () => { await invalidateTrash(); setPendingPurge(null); },
    onError: (error) => onNotice(error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), 'error')
  });
  const empty = useMutation({
    mutationFn: (value: string) => api.emptyTrash(value),
    onSuccess: async () => { await invalidateTrash(); setPendingPurge(null); },
    onError: (error) => onNotice(error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), 'error')
  });

  const displayedEntries = cursor ? entries : trashQuery.data?.entries ?? entries;
  const displayedNextCursor = cursor ? nextCursor : trashQuery.data?.nextCursor ?? nextCursor;
  const isPurging = purge.isPending || empty.isPending;
  const confirmPurge = () => {
    if (confirmation !== 'DELETE' || !pendingPurge) return;
    if (pendingPurge.type === 'entry') purge.mutate({ id: pendingPurge.entry.id, confirmation });
    else empty.mutate(confirmation);
  };

  return <>
    <SidePanel
      open={open}
      onClose={onClose}
      icon={<DeleteOutline />}
      title={t('recycleBin.title')}
      trailing={mutable && displayedEntries.length > 0 ? <Button size="small" color="error" onClick={() => setPendingPurge({ type: 'empty' })}>{t('action.emptyRecycleBin')}</Button> : undefined}
    >
      <Stack spacing={2}>
        {open && <DirectorySize path="" kind="trash" />}
        {trashQuery.isLoading && displayedEntries.length === 0 ? <Typography color="text.secondary">{t('recycleBin.loading')}</Typography> : null}
        {trashQuery.isError && displayedEntries.length === 0 ? <Alert severity="error">{trashQuery.error instanceof ApiError ? t(`error.${trashQuery.error.code}`) : t('error.generic')}</Alert> : null}
        {!trashQuery.isLoading && !trashQuery.isError && displayedEntries.length === 0 ? <EmptyState icon={<DeleteOutline />} title={t('recycleBin.emptyTitle')} caption={t('recycleBin.emptyHint')} /> : null}
        {displayedEntries.length > 0 ? <List disablePadding aria-label={t('recycleBin.title')}>
          {displayedEntries.map((entry, index) => <Box key={entry.id}>
            {index > 0 ? <Divider component="li" /> : null}
            <ListItem alignItems="flex-start" disableGutters secondaryAction={mutable ? <Stack direction="row" spacing={0.25}>
              <Button size="small" startIcon={<RestoreOutlined />} disabled={restore.isPending} onClick={() => restore.mutate(entry)}>{t('action.restore')}</Button>
              <Button size="small" color="error" aria-label={`${t('action.permanentlyDelete')} ${entry.name}`} disabled={isPurging} onClick={() => setPendingPurge({ type: 'entry', entry })}><DeleteForeverOutlined fontSize="small" /></Button>
            </Stack> : undefined} sx={{ pr: mutable ? 14 : 0, py: 1.5 }}>
              <ListItemIcon sx={{ minWidth: 34, color: entry.kind === 'directory' ? 'primary.light' : 'text.secondary' }}><DeleteOutline fontSize="small" /></ListItemIcon>
              <ListItemText
                primary={<Typography variant="bodyStrong" sx={{ overflowWrap: 'anywhere' }}>{entry.name}</Typography>}
                secondary={<Stack component="span" spacing={0.25} sx={{ mt: 0.5 }}>
                  <Typography component="span" variant="caption" color="text.secondary">{t('recycleBin.originalLocation')}: {entry.originalPath}</Typography>
                  <Typography component="span" variant="caption" color="text.secondary">{formatBytes(entry.sizeBytes)} · {formatDeletedAt(entry.deletedAt)}</Typography>
                </Stack>}
              />
            </ListItem>
          </Box>)}
        </List> : null}
        {displayedNextCursor ? <Button variant="outlined" disabled={trashQuery.isFetching} onClick={() => setCursor(displayedNextCursor)}>{t('action.loadMore')}</Button> : null}
      </Stack>
    </SidePanel>
    <DialogShell
      open={Boolean(pendingPurge)}
      onClose={() => setPendingPurge(null)}
      title={pendingPurge?.type === 'entry' ? t('dialog.confirmPermanentDelete', { name: pendingPurge.entry.name }) : t('dialog.confirmEmptyRecycleBin')}
      confirmLabel={t('action.permanentlyDelete')}
      confirmTone="destructive"
      confirmDisabled={confirmation !== 'DELETE' || isPurging}
      onConfirm={confirmPurge}
    >
      <Typography color="text.secondary">{t('dialog.permanentDeleteText')}</Typography>
      <TextField autoFocus fullWidth label={t('dialog.typeToConfirm')} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} />
    </DialogShell>
  </>;
}
