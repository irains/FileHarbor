import { useEffect, useRef, useState } from 'react';
import { FolderOutlined, StarBorderOutlined } from '@mui/icons-material';
import { Alert, Button, List, ListItem, Skeleton, Stack, TextField, Typography } from '@mui/material';
import { api, ApiError, type Favorite } from '../api/client';
import { useI18n } from '../i18n';
import { SidePanel } from './SidePanel';
import { FavoriteButton } from './FavoriteButton';
import { useFavorites } from './useFavorites';

export function FavoritesPanel({ directory, onClose, onNavigate }: { directory: string; onClose: () => void; onNavigate: (path: string) => void }) {
  const { t } = useI18n();
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const { query, pending, change, error } = useFavorites();
  const [editing, setEditing] = useState<Favorite | null>(null);
  const [editLabel, setEditLabel] = useState('');
  const [opening, setOpening] = useState<string | null>(null);
  const openingRef = useRef(false);
  const [openError, setOpenError] = useState<{ id: string; code: string } | null>(null);
  const renameButton = useRef<HTMLButtonElement | null>(null);
  const finishEditing = () => { setEditing(null); requestAnimationFrame(() => { if (mounted.current) renameButton.current?.focus(); }); };
  const open = async (entry: Favorite) => {
    if (openingRef.current) return;
    openingRef.current = true;
    setOpening(entry.id); setOpenError(null);
    try {
      // Never navigate to a fallback returned for an invalid favorite.
      const result = await api.getDirectories(entry.path);
      if (result.path !== entry.path) throw new ApiError(404, 'not_found');
      if (mounted.current) onNavigate(entry.path);
    } catch (failure) {
      if (mounted.current) setOpenError({ id: entry.id, code: failure instanceof ApiError ? failure.code : 'generic' });
    } finally {
      openingRef.current = false;
      if (mounted.current) setOpening(null);
      void query.refetch();
    }
  };
  const busy = pending || opening !== null;
  return <SidePanel open onClose={onClose} icon={<StarBorderOutlined />} title={t('favorites.title')}>
    <Stack spacing={2} sx={{ '& .MuiButton-root': { minHeight: 44 } }}>
      <Stack spacing={1}>
        <Typography variant="body2" color="text.secondary" sx={{ overflowWrap: 'anywhere' }}>{directory || t('workspace.root')}</Typography>
        <FavoriteButton directory={directory} />
      </Stack>
      {error && <Alert severity="error">{t(`error.${error}`)}</Alert>}
      {query.isPending && <Stack spacing={1} aria-label={t('favorites.loading')}><Skeleton height={44} /><Skeleton height={44} /></Stack>}
      <List disablePadding>
        {query.data?.entries.map((entry) => <ListItem key={entry.id} disableGutters sx={{ display: 'block', py: 1.5, borderBottom: '1px solid', borderColor: 'divider' }}>
          <Stack spacing={0.5}>
            <Stack direction="row" spacing={1} alignItems="center">
              <FolderOutlined sx={{ color: 'text.secondary', flexShrink: 0 }} />
              <Typography variant="body2" sx={{ fontWeight: 600, overflowWrap: 'anywhere', minWidth: 0 }}>{entry.label}</Typography>
            </Stack>
            <Typography variant="caption" color="text.secondary" sx={{ overflowWrap: 'anywhere' }}>{entry.path || t('workspace.root')}</Typography>
            {entry.path === directory && <Typography color="primary.main" variant="caption">{t('favorites.current')}</Typography>}
            {entry.availability !== 'available' && <Typography color="warning.main" variant="caption">{t(`favorites.${entry.availability || 'unavailable'}`)}</Typography>}
            {openError?.id === entry.id && <Alert severity="error">{t(`error.${openError.code}`)}</Alert>}
            {editing?.id === entry.id && <Stack component="form" spacing={1} onSubmit={(event) => {
              event.preventDefault();
              const target = { ...editing };
              void change({ kind: 'rename', entry: target, label: editLabel.trim() }).then(success => {
                if (success && mounted.current) finishEditing();
              });
            }}>
              <TextField autoFocus label={t('favorites.label')} value={editLabel} onChange={(event) => setEditLabel(event.target.value)} size="small" required disabled={pending}
                onKeyDown={(event) => { if (event.key === 'Escape') { event.stopPropagation(); if (!pending) finishEditing(); } }} />
              <Stack direction="row" gap={1} flexWrap="wrap">
                <Button type="submit" disabled={busy || !editLabel.trim()}>{t('favorites.save')}</Button>
                <Button disabled={pending} onClick={finishEditing}>{t('action.cancel')}</Button>
              </Stack>
            </Stack>}
            <Stack direction="row" flexWrap="wrap" gap={0.5}>
              <Button variant="outlined" disabled={busy} aria-busy={opening === entry.id} onClick={() => void open({ ...entry })}>{t('favorites.open')}</Button>
              <Button disabled={busy || editing?.id === entry.id} onClick={(event) => { renameButton.current = event.currentTarget; setEditing({ ...entry }); setEditLabel(entry.label); }}>{t('favorites.rename')}</Button>
              <Button color="inherit" disabled={busy || editing?.id === entry.id} onClick={() => void change({ kind: 'remove', entry: { ...entry } })}>{t('favorites.remove')}</Button>
            </Stack>
          </Stack>
        </ListItem>)}
      </List>
      {query.data?.entries.length === 0 && <Typography variant="body2" color="text.secondary">{t('favorites.empty')}</Typography>}
    </Stack>
  </SidePanel>;
}
