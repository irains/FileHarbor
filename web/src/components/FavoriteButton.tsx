import { StarBorderOutlined, StarOutlined } from '@mui/icons-material';
import { Alert, Button, Stack } from '@mui/material';
import { useI18n } from '../i18n';
import { useFavorites } from './useFavorites';

export function FavoriteButton({ directory, selected = false }: { directory: string; selected?: boolean }) {
  const { t } = useI18n();
  const { query, pending, change, error, errorPath } = useFavorites();
  const entry = query.data?.entries.find(item => item.path === directory);
  const known = query.data !== undefined;
  const localError = errorPath === directory ? error : null;
  return <Stack spacing={1} sx={{ minWidth: 0, maxWidth: '100%' }}>
    <Button variant="outlined"
      startIcon={entry ? <StarOutlined /> : <StarBorderOutlined />}
      aria-pressed={known ? Boolean(entry) : undefined}
      aria-busy={pending || query.isPending}
      title={entry ? t('favorites.remove') : undefined}
      disabled={pending || query.isPending || query.isError || !known}
      sx={{ minHeight: 44 }}
      onClick={() => {
        // Capture target and revision now, not after the network response.
        void change(entry ? { kind: 'remove', entry: { ...entry } } : {
          kind: 'add', path: directory, label: directory.split('/').pop() || t('workspace.root'),
        });
      }}
    >{!known ? t(query.isError ? 'favorites.unavailable' : 'favorites.loading') : entry ? t('favorites.saved') : t(selected ? 'favorites.addSelected' : 'favorites.add')}</Button>
    {(localError || query.isError) && <Alert severity="error" sx={{ overflowWrap: 'anywhere' }} action={query.isError ?
      <Button color="inherit" sx={{ minHeight: 44 }} onClick={() => void query.refetch()}>{t('action.retry')}</Button> : undefined
    }>{t(`error.${localError || 'favorites_unavailable'}`)}</Alert>}
  </Stack>;
}
