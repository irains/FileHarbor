import { useEffect, useRef, useState } from 'react';
import { StarBorderOutlined } from '@mui/icons-material';
import { Alert, Button, CircularProgress, List, ListItem, Stack, TextField, Typography } from '@mui/material';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type Favorite } from '../api/client';
import { useI18n } from '../i18n';
import { SidePanel } from './SidePanel';

export function FavoritesPanel({ directory, onClose, onNavigate }: { directory: string; onClose: () => void; onNavigate: (path: string) => void }) {
  const { t } = useI18n();
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const client = useQueryClient();
  const query = useQuery({ queryKey: ['favorites'], queryFn: api.getFavorites, staleTime: 0 });
  const [label, setLabel] = useState(directory.split('/').pop() || t('workspace.root'));
  const [editing, setEditing] = useState<Favorite | null>(null);
  const [editLabel, setEditLabel] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const run = async (operation: () => Promise<unknown>) => {
    setBusy(true); setError(null);
    try { await operation(); setEditing(null); }
    catch (failure) { setError(failure instanceof ApiError ? failure.code : 'generic'); }
    finally { await client.invalidateQueries({ queryKey: ['favorites'] }); setBusy(false); }
  };
  const open = (entry: Favorite) => run(async () => {
    // Revalidate before navigation; stale favorites must not redirect elsewhere.
    await api.getDirectories(entry.path);
    if (mounted.current) onNavigate(entry.path);
  });
  return <SidePanel open onClose={onClose} icon={<StarBorderOutlined />} title={t('favorites.title')}>
    <Stack spacing={2}>
      <Typography variant="body2" sx={{ overflowWrap: 'anywhere' }}>{directory || t('workspace.root')}</Typography>
      <Stack component="form" spacing={1} onSubmit={(event) => { event.preventDefault(); void run(() => api.addFavorite(directory, label)); }}>
        <TextField label={t('favorites.label')} value={label} onChange={(event) => setLabel(event.target.value)} size="small" required />
        <Button type="submit" variant="outlined" disabled={busy || !label.trim()}>{t('favorites.add')}</Button>
      </Stack>
      {(error || query.isError) && <Alert severity="error" action={query.isError ? <Button color="inherit" onClick={() => void query.refetch()}>{t('action.retry')}</Button> : undefined}>{t(`error.${error || 'favorites_unavailable'}`)}</Alert>}
      {query.isPending && <CircularProgress size={24} />}
      <List disablePadding>
        {query.data?.entries.map((entry) => <ListItem key={entry.id} disableGutters sx={{ display: 'block', py: 1.5, borderBottom: '1px solid', borderColor: 'divider' }}>
          <Stack spacing={1}>
            <Typography sx={{ overflowWrap: 'anywhere' }}>{entry.label}</Typography>
            <Typography variant="caption" sx={{ overflowWrap: 'anywhere' }}>{entry.path || t('workspace.root')}</Typography>
            {entry.availability !== 'available' && <Typography color="warning.main" variant="caption">{t(`favorites.${entry.availability || 'unavailable'}`)}</Typography>}
            {editing?.id === entry.id ? <Stack component="form" spacing={1} onSubmit={(event) => { event.preventDefault(); void run(() => api.renameFavorite(entry, editLabel)); }}>
              <TextField label={t('favorites.label')} value={editLabel} onChange={(event) => setEditLabel(event.target.value)} size="small" required />
              <Button type="submit" disabled={busy || !editLabel.trim()}>{t('favorites.save')}</Button>
              <Button onClick={() => setEditing(null)}>{t('action.cancel')}</Button>
            </Stack> : <Stack direction="row" flexWrap="wrap" gap={1}>
              <Button disabled={busy} onClick={() => void open(entry)}>{t('favorites.open')}</Button>
              <Button disabled={busy} onClick={() => { setEditing(entry); setEditLabel(entry.label); }}>{t('favorites.rename')}</Button>
              <Button disabled={busy} onClick={() => void run(() => api.removeFavorite(entry))}>{t('favorites.remove')}</Button>
            </Stack>}
          </Stack>
        </ListItem>)}
      </List>
      {query.data?.entries.length === 0 && <Typography>{t('favorites.empty')}</Typography>}
    </Stack>
  </SidePanel>;
}
