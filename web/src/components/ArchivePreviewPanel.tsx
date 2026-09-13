import { DescriptionOutlined, FolderOutlined, FolderZipOutlined } from '@mui/icons-material';
import { Alert, Box, Button, CircularProgress, List, ListItem, ListItemIcon, ListItemText, Stack, Typography } from '@mui/material';
import { useQuery } from '@tanstack/react-query';
import { api, ApiError, type ArchivePreview, type FileEntry } from '../api/client';
import { formatBytes } from '../formatBytes';
import { useI18n } from '../i18n';
import { SidePanel } from './SidePanel';

export function ArchivePreviewPanel({ entry, onClose }: { entry: FileEntry | null; onClose: () => void }) {
  const { t } = useI18n();
  const query = useQuery({
    queryKey: ['archive-preview', entry?.path, entry?.version],
    queryFn: ({ signal }) => api.previewArchive(entry!.path, entry!.version, signal),
    enabled: Boolean(entry),
    retry: false,
    gcTime: 0
  });
  const preview = query.data?.preview;
  return <SidePanel open={Boolean(entry)} onClose={onClose} icon={<FolderZipOutlined />} title={t('archivePreview.title')}>
    <Stack spacing={2}>
      <Typography variant="body2" sx={{ overflowWrap: 'anywhere' }}>{entry?.path}</Typography>
      <Alert severity="info">{t('archivePreview.metadata')}</Alert>
      {query.isPending && <Box role="status" aria-label={t('archivePreview.loading')}><CircularProgress size={24} /></Box>}
      {query.isError && <Alert severity="error" action={<Button color="inherit" onClick={() => void query.refetch()}>{t('archivePreview.retry')}</Button>}>
        {query.error instanceof ApiError ? t(`error.${query.error.code}`) : t('error.generic')}
      </Alert>}
      {preview && <>
        <Typography variant="body2" role="status">{t('archivePreview.count', { count: preview.entries.length })}</Typography>
        {!preview.complete && <Alert severity="warning">{t('archivePreview.incomplete')} {t(`error.${preview.reason}`)}</Alert>}
        <ArchiveTree entries={preview.entries} />
      </>}
    </Stack>
  </SidePanel>;
}

interface ArchiveNode {
  path: string;
  entry?: ArchivePreview['entries'][number];
  children: Map<string, ArchiveNode>;
}

function ArchiveTree({ entries }: { entries: ArchivePreview['entries'] }) {
  const { t } = useI18n();
  const root: ArchiveNode = { path: '', children: new Map() };
  for (const entry of entries) {
    let node = root;
    for (const part of entry.name.split('/').filter(Boolean)) {
      let child = node.children.get(part);
      if (!child) {
        child = { path: node.path ? `${node.path}/${part}` : part, children: new Map() };
        node.children.set(part, child);
      }
      node = child;
    }
    node.entry = entry;
  }
  const render = (nodes: Map<string, ArchiveNode>, depth: number) => <List dense disablePadding sx={{ pl: depth ? 1 : 0 }}>
    {[...nodes.values()].map(node => {
      const directory = node.children.size > 0 || node.entry?.kind === 'directory';
      return <ListItem key={node.path} disableGutters sx={{ display: 'block' }}>
        {directory ? <Box component="details" open={depth === 0}>
          <Box component="summary" sx={{ cursor: 'pointer', minHeight: 44, py: 1, overflowWrap: 'anywhere' }}>
            <FolderOutlined fontSize="small" sx={{ verticalAlign: 'middle', mr: 1 }} />{node.path}
          </Box>
          {render(node.children, depth + 1)}
        </Box> : <Box sx={{ display: 'flex', alignItems: 'flex-start' }}>
          <ListItemIcon sx={{ minWidth: 32, pt: 0.5 }}><DescriptionOutlined fontSize="small" /></ListItemIcon>
          <ListItemText primary={node.path} secondary={node.entry?.size === undefined ? t('archivePreview.unknown') : t('archivePreview.declared', { size: formatBytes(node.entry.size) })}
            slotProps={{ primary: { sx: { overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' } } }} />
        </Box>}
      </ListItem>;
    })}
  </List>;
  return <Box aria-label={t('archivePreview.title')}>{render(root.children, 0)}</Box>;
}
