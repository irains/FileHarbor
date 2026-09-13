import { FavoriteButton } from './FavoriteButton';
import { useTasks } from '../tasks/TaskProvider';
import { ExtractArchiveDialog } from './ExtractArchiveDialog';
import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import {
  Add,
  ArrowUpward,
  Archive,
  Close,
  ContentCopy,
  CreateNewFolder,
  DeleteOutline,
  DescriptionOutlined,
  DownloadOutlined,
  Folder,
  FolderOffOutlined,
  InfoOutlined,
  LogoutOutlined,
  MoreVert,
  Refresh,
  SettingsOutlined,
  TaskAlt,
  StarBorderOutlined,
  UploadFile
} from '@mui/icons-material';
import {
  Alert,
  type AlertColor,
  AppBar,
  Box,
  Breadcrumbs,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Divider,
  IconButton,
  ListItemIcon,
  Menu,
  MenuItem,
  Paper,
  Skeleton,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Toolbar,
  Tooltip,
  Typography,
  useMediaQuery
} from '@mui/material';
import { useTheme } from '@mui/material/styles';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link, useLocation, useMatches, useNavigate } from 'react-router-dom';
import { api, ApiError, type FileEntry, type Properties } from '../api/client';
import { directoryRoute, editorRoute, itemUrl } from '../runtime';
import { useI18n } from '../i18n';
import { useSession } from '../session/SessionProvider';
import { Mark } from './Mark';
import { DialogShell } from './DialogShell';
import { SidePanel } from './SidePanel';
import { EmptyState } from './EmptyState';
import { UploadQueueDrawer } from './UploadQueueDrawer';
import { SettingsPanel } from './SettingsPanel';
import { RecycleBinPanel } from './RecycleBinPanel';
import { DirectorySize } from './DirectorySize';
import { FavoritesPanel } from './FavoritesPanel';
import { SearchPanel } from './SearchPanel';
import { ArchivePreviewPanel } from './ArchivePreviewPanel';
import { entryMenuActions, hoverActionNames, type EntryAction, type EntryActionName } from './entryActions';
import { formatBytes } from '../formatBytes';
import { FolderDestinationPicker } from './FolderDestinationPicker';
import { fontFamilyMono, surface } from '../tokens';
import { EMPTY_ENTRIES, filterAndSortEntries, listingSelectionState, type ListingKindFilter, type ListingSort } from './workspaceListing';

const LazyEditorDialog = lazy(() => import('../editor/EditorDialog').then((module) => ({ default: module.EditorDialog })));
type FormAction = 'newdir' | 'newfile' | 'rename' | 'move' | 'copy';
type FormState = { action: FormAction; entry?: FileEntry } | null;

const fmtDate = (value: string | number) => value ? new Date(value).toLocaleString() : '—';
export const entryKindLabel = (kind: FileEntry['kind'], t: (key: string) => string) => t(kind === 'directory' ? 'workspace.folder' : 'workspace.file');
export const desktopTableColumnSx = {
  name: { width: '100%' },
  size: { width: '1%', whiteSpace: 'nowrap' },
  modified: { width: '1%', whiteSpace: 'nowrap' }
} as const;
export const fileNameButtonSx = {
  appearance: 'none',
  background: 'none',
  border: 0,
  p: 0,
  textAlign: 'left',
  cursor: 'pointer',
  font: 'inherit',
  overflowWrap: 'anywhere'
} as const;
export const mobileFileNameSx = {
  ...fileNameButtonSx,
  display: '-webkit-box',
  overflow: 'hidden',
  WebkitBoxOrient: 'vertical',
  WebkitLineClamp: 2
} as const;

export function decodeRouteSplat(splat: string | undefined): string {
  return (splat ?? '').split('/').filter(Boolean).join('/');
}

export function directoryPathForEditor(editorPath: string | null, directoryPath = ''): string {
  if (editorPath) {
    const segments = editorPath.split('/').filter(Boolean);
    return segments.slice(0, -1).join('/');
  }
  return directoryPath;
}

function RowActions({ entry, mutable, editorAvailable, compact = false, onAction, onMenu }: { entry: FileEntry; mutable: boolean; editorAvailable: boolean; compact?: boolean; onAction: (name: EntryActionName) => void; onMenu: (event: React.MouseEvent<HTMLElement>) => void }) {
  const { t } = useI18n();
  const visible = compact ? [] : hoverActionNames.filter((name) => entryMenuActions(entry, mutable, editorAvailable).some((action) => action.name === name));
  return (
    <Stack direction="row" spacing={0.5} alignItems="center" justifyContent="flex-end">
      {visible.map((name) => {
        const Icon = name === 'download' ? DownloadOutlined : InfoOutlined;
        const label = t(`action.${name}`);
        return (
          <Tooltip key={name} title={label}>
            <IconButton size="small" aria-label={`${label} ${entry.name}`} onClick={() => onAction(name)}>
              <Icon fontSize="small" />
            </IconButton>
          </Tooltip>
        );
      })}
      <Tooltip title={t('workspace.actions')}>
        <IconButton size="small" aria-label={`${t('workspace.actions')} ${entry.name}`} onClick={onMenu}>
          <MoreVert fontSize="small" />
        </IconButton>
      </Tooltip>
    </Stack>
  );
}

function MobileFileList({
  parentPath,
  entries,
  selected,
  selectionState,
  mutable,
  editorAvailable,
  onSelect,
  onSelectAll,
  onAction,
  onMenu,
  emptyState,
  busy = false
}: {
  parentPath: string | null;
  entries: FileEntry[];
  selected: ReadonlySet<string>;
  selectionState: ReturnType<typeof listingSelectionState>;
  mutable: boolean;
  editorAvailable: boolean;
  onSelect: (entry: FileEntry, checked: boolean) => void;
  onSelectAll: (checked: boolean) => void;
  onAction: (name: EntryActionName, entry: FileEntry) => void;
  onMenu: (entry: FileEntry, anchor: HTMLElement) => void;
  emptyState?: ReactNode;
  busy?: boolean;
}) {
  const { t } = useI18n();
  return (
    <Paper aria-busy={busy || undefined} sx={{ ...surface, overflow: 'hidden' }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.75, px: 1.5, py: 0.75, borderBottom: '1px solid', borderColor: 'divider' }}>
        <Checkbox
          size="small"
          aria-label={t('workspace.selectAll')}
          checked={selectionState.allSelected}
          indeterminate={selectionState.partiallySelected}
          onChange={(event) => onSelectAll(event.target.checked)}
        />
        <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 700, letterSpacing: '0.04em', textTransform: 'uppercase' }}>{t('workspace.name')}</Typography>
      </Box>
      <Box aria-label={t('app.workspace')}>
        {parentPath !== null && <Box sx={{ px: 1.5, borderBottom: '1px solid', borderColor: 'divider' }}>
          <Button component={Link} to={directoryRoute(parentPath)} aria-label={t('workspace.parentDirectory')} title={t('workspace.parentDirectory')} startIcon={<ArrowUpward fontSize="small" />} color="inherit" sx={{ minHeight: 44, justifyContent: 'flex-start', width: '100%', fontWeight: 700 }}>{t('workspace.upOneLevel')}</Button>
        </Box>}
        {entries.map((entry, index) => (
          <Box key={entry.path} sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.75, px: 1.5, py: 1.25, borderTop: index ? '1px solid' : undefined, borderColor: 'divider', bgcolor: selected.has(entry.path) ? 'action.selected' : undefined }}>
            <Checkbox size="small" sx={{ mt: -0.5, ml: -0.75 }} aria-label={t('workspace.selectItem', { name: entry.name })} checked={selected.has(entry.path)} onChange={(event) => onSelect(entry, event.target.checked)} />
            <ListItemIcon sx={{ minWidth: 28, mt: 0.1, color: entry.kind === 'directory' ? 'primary.light' : 'text.secondary' }}>
              {entry.kind === 'directory' ? <Folder fontSize="small" /> : <DescriptionOutlined fontSize="small" />}
            </ListItemIcon>
            <Box sx={{ minWidth: 0, flex: 1, pt: 0.05 }}>
              <Typography
                component={entry.kind === 'directory' ? Link : 'button'}
                to={entry.kind === 'directory' ? directoryRoute(entry.path) : undefined}
                type={entry.kind === 'directory' ? undefined : 'button'}
                onClick={entry.kind === 'file' ? () => window.location.assign(itemUrl('download', entry.path)) : undefined}
                color="inherit"
                fontWeight={entry.kind === 'directory' ? 700 : 500}
                title={entry.name}
                aria-label={entry.name}
                sx={mobileFileNameSx}
              >
                {entry.name}
              </Typography>
              <Typography variant="caption" color="text.secondary" component="div" sx={{ mt: 0.35, overflowWrap: 'anywhere' }}>
                {entry.kind === 'file' ? formatBytes(entry.sizeBytes) : entryKindLabel(entry.kind, t)} · {fmtDate(entry.modifiedAt)}
              </Typography>
            </Box>
            <RowActions
              compact
              entry={entry}
              mutable={mutable}
              editorAvailable={editorAvailable}
              onAction={(name) => onAction(name, entry)}
              onMenu={(event) => onMenu(entry, event.currentTarget)}
            />
          </Box>
        ))}
      </Box>
      {!entries.length ? emptyState : null}
    </Paper>
  );
}

export function Workspace() {
  const theme = useTheme();
  const { t } = useI18n();
  const { session, logout } = useSession();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const routePathname = location.pathname.replace(/\/$/, '') || '/';
  const matches = useMatches();
  const routeParams = matches.at(-1)?.params ?? {};
  const editorPath = /^\/edit(?:\/|$)/.test(routePathname) ? decodeRouteSplat(routeParams['*']) || null : null;
  const currentPath = directoryPathForEditor(editorPath, /^\/d(?:\/|$)/.test(routePathname) ? decodeRouteSplat(routeParams['*']) : '');
  const previousPath = useRef(currentPath);
  const previousListingToken = useRef<string | undefined>(undefined);
  const previousListingControls = useRef<{ query: string; kind: ListingKindFilter; sort: ListingSort } | null>(null);
  const currentPathRef = useRef(currentPath);
  currentPathRef.current = currentPath;
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [menuEntry, setMenuEntry] = useState<FileEntry | null>(null);
  const [menuAnchor, setMenuAnchor] = useState<HTMLElement | null>(null);
  const [form, setForm] = useState<FormState>(null);
  const tasks = useTasks();
  const [extractFor, setExtractFor] = useState<FileEntry | null>(null);
  const [archivePreviewFor, setArchivePreviewFor] = useState<FileEntry | null>(null);
  const [propertiesFor, setPropertiesFor] = useState<FileEntry | null>(null);
  const [showUploads, setShowUploads] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [recycleBinOpen, setRecycleBinOpen] = useState(false);
  const [notice, setNotice] = useState<{ message: string; severity: AlertColor } | null>(null);
  const [manualRefresh, setManualRefresh] = useState(false);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [favoritesOpen, setFavoritesOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [kindFilter, setKindFilter] = useState<ListingKindFilter>('all');
  const [listingSort, setListingSort] = useState<ListingSort>('folders-first');
  const [pendingDelete, setPendingDelete] = useState<{ type: 'single'; entry: FileEntry } | { type: 'batch'; entries: FileEntry[] } | null>(null);
  const compact = useMediaQuery(theme.breakpoints.down('md'));
  const mobile = useMediaQuery(theme.breakpoints.down('sm'));
  const listingQuery = useQuery({ queryKey: ['listing', currentPath], queryFn: () => api.getListing(currentPath) });
  const listing = listingQuery.data;
  const isRefreshing = listingQuery.isFetching && !listingQuery.isLoading;
  const refreshInProgress = manualRefresh || isRefreshing;
  const listingToken = listing?.listingToken;
  const entries = listing?.entries ?? EMPTY_ENTRIES;
  const selectedForCurrentToken = previousListingToken.current === undefined || previousListingToken.current === listingToken ? selected : new Set<string>();
  const displayedEntries = useMemo(() => filterAndSortEntries(entries, { query: searchQuery, kind: kindFilter, sort: listingSort }), [entries, searchQuery, kindFilter, listingSort]);
  const routeEditor = editorPath ? {
    name: editorPath.split('/').pop() ?? editorPath,
    path: editorPath,
    kind: 'file' as const,
    sizeBytes: 0,
    modifiedAt: '',
    mode: '',
    extension: '',
    isArchive: false,
    previewable: false,
    editable: true,
    version: ''
  } : null;
  const activeEditor = routeEditor;
  const editorReturnPath = directoryPathForEditor(editorPath);
  const mutable = Boolean(session?.capabilities.mutate);
  const canUpload = Boolean(session?.capabilities.upload);

  const refreshDirectory = useCallback(async (path: string) => {
    await queryClient.invalidateQueries({ queryKey: ['listing', path] });
  }, [queryClient]);
  const handleManualRefresh = async () => {
    if (refreshInProgress) return;
    const refreshPath = currentPath;
    setManualRefresh(true);
    setRefreshError(null);
    setSelected(new Set());
    try {
      await queryClient.invalidateQueries({ queryKey: ['listing', refreshPath] }, { throwOnError: true, cancelRefetch: false });
      if (currentPathRef.current === refreshPath) setNotice({ message: t('workspace.refreshed'), severity: 'success' });
    } catch (error) {
      if (currentPathRef.current === refreshPath) setRefreshError(error instanceof ApiError ? t(`error.${error.code}`) : t('workspace.refreshFailed'));
    } finally {
      if (currentPathRef.current === refreshPath) setManualRefresh(false);
    }
  };
  const mutation = useMutation({
    mutationFn: ({ endpoint, values }: { endpoint: string; values: Record<string, string>; affectedDirectories: string[] }) => api.mutate<{ ok: true; hash?: string }>(endpoint, values),
    onSuccess: async (result, variables) => {
      if (result.hash) setNotice({ message: t('success.checksum', { hash: result.hash }), severity: 'success' });
      await Promise.all(variables.affectedDirectories.map(refreshDirectory));
    },
    onError: (error) => setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' }),
    onSettled: async (_result, _error, variables) => {
      // The bin may already be open; failed operations can also leave recycled content.
      if (variables.endpoint === 'do/rm') await queryClient.invalidateQueries({ queryKey: ['trash'] });
    }
  });
  const batch = useMutation({
    mutationFn: ({ endpoint, body }: { endpoint: string; body: unknown; affectedDirectories: string[] }) => api.batch<{ ok: boolean; download_url?: string }>(endpoint, body),
    onSuccess: async (result, variables) => { if (result.download_url) window.location.assign(result.download_url); else await Promise.all(variables.affectedDirectories.map(refreshDirectory)); },
    onError: (error) => setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' }),
    onSettled: async (_result, _error, variables) => {
      if (variables.endpoint === 'do/batch/delete') await queryClient.invalidateQueries({ queryKey: ['trash'] });
    }
  });
  const propertyQuery = useQuery({ queryKey: ['properties', propertiesFor?.path], queryFn: () => api.getProperties(propertiesFor!.path), enabled: Boolean(propertiesFor) });

  useEffect(() => {
    if (previousPath.current === currentPath) return;
    previousPath.current = currentPath;
    setExtractFor(null);
    setSelected(new Set());
    setMenuEntry(null);
    setMenuAnchor(null);
    setForm(null);
    setPropertiesFor(null);
    setArchivePreviewFor(null);
    setPendingDelete(null);
    setManualRefresh(false);
    setRefreshError(null);
    setSearchQuery('');
    setSearchOpen(false);
    setFavoritesOpen(false);
    setKindFilter('all');
    setListingSort('folders-first');
  }, [currentPath]);
  useEffect(() => {
    if (previousListingToken.current !== undefined && previousListingToken.current !== listingToken) setSelected(new Set());
    previousListingToken.current = listingToken;
  }, [listingToken]);
  useEffect(() => {
    const nextControls = { query: searchQuery, kind: kindFilter, sort: listingSort };
    if (previousListingControls.current !== null
      && (previousListingControls.current.query !== nextControls.query
        || previousListingControls.current.kind !== nextControls.kind
        || previousListingControls.current.sort !== nextControls.sort)) setSelected(new Set());
    previousListingControls.current = nextControls;
  }, [searchQuery, kindFilter, listingSort]);
  const updateListingControls = (update: () => void) => {
    setSelected(new Set());
    update();
  };
  const select = (entry: FileEntry, checked: boolean) => setSelected((previous) => { const next = new Set(previous); if (checked) next.add(entry.path); else next.delete(entry.path); return next; });
  const selectionState = listingSelectionState(displayedEntries, selectedForCurrentToken);
  const { selectedEntries } = selectionState;
  const batchBody = (destination?: string) => ({ listing_token: listing?.listingToken, entries: selectedEntries.map(({ name, version }) => ({ name, version })), ...(destination !== undefined ? { destination } : {}) });
  const affectedDirectories = (destination?: string) => [...new Set([currentPath, ...(destination === undefined ? [] : [destination])])];
  const doBatch = (endpoint: string, destination?: string) => {
    if (endpoint === 'do/batch/copy' && tasks && listing?.listingToken) { void tasks.submit({ kind: 'copy', ...batchBody(destination) }).catch(error => setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' })); return; }
    if (listing?.listingToken && selectedEntries.length) batch.mutate({ endpoint, body: batchBody(destination), affectedDirectories: affectedDirectories(destination) });
  };
  const performEntryAction = (action: EntryActionName, entry: FileEntry) => {
    setMenuEntry(null); setMenuAnchor(null);
    if (action === 'extract') return setExtractFor(entry);
    if (action === 'archive' && tasks) { void tasks.submit({ kind: 'compress', path: entry.path, version: entry.version }).catch(error => setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' })); return; }
    if (action === 'archivePreview') return setArchivePreviewFor(entry);
    if (action === 'properties') return setPropertiesFor(entry);
    if (action === 'edit') return navigate(editorRoute(entry.path), { state: { editorOrigin: location.pathname } });
    if (action === 'download') return window.location.assign(itemUrl('download', entry.path));
    if (action === 'preview') return window.location.assign(itemUrl('view', entry.path));
    if (action === 'rename') return setForm({ action: 'rename', entry });
    if (action === 'move' || action === 'copy') return setForm({ action, entry });
    if (action === 'trash') return setPendingDelete({ type: 'single', entry });
    const endpoints: Record<string, string> = { archive: 'do/zip', extract: 'do/extract', checksum: 'do/md5' };
    if (action in endpoints) mutation.mutate({ endpoint: endpoints[action], values: { path: entry.path }, affectedDirectories: affectedDirectories() });
  };
  const signOut = async () => {
    try { await logout(); window.location.assign(itemUrl('d', '')); }
    catch (error) { setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' }); }
  };
  const confirmDelete = () => {
    if (!pendingDelete) return;
    if (pendingDelete.type === 'single') {
      mutation.mutate({ endpoint: 'do/rm', values: { path: pendingDelete.entry.path }, affectedDirectories: affectedDirectories() });
    } else {
      doBatch('do/batch/delete');
    }
    setPendingDelete(null);
  };

  const breadcrumbs = useMemo(() => currentPath.split('/').filter(Boolean), [currentPath]);
  const closeEditor = () => {
    const state = location.state as { editorOrigin?: unknown } | null;
    const origin = typeof state?.editorOrigin === 'string'
      && (state.editorOrigin === '/' || state.editorOrigin.startsWith('/d/'))
      ? state.editorOrigin
      : null;
    if (origin) navigate(origin, { replace: true });
    else navigate(directoryRoute(editorReturnPath), { replace: true });
  };

  const listingContent = listingQuery.isLoading ? (
    <Stack aria-label={t('editor.loading')} sx={{ width: '100%' }}>
      <Box sx={{ maxWidth: 1440, mx: 'auto', px: { xs: 2, sm: 3 }, py: 3, width: '100%' }}>
        <Skeleton variant="text" sx={{ width: 240, height: 36 }} />
        <Skeleton variant="text" sx={{ width: 160, height: 20, mt: 1 }} />
        <TableContainer component={Paper} sx={{ ...surface, mt: 3 }}>
          <Table>
            <TableBody>{Array.from({ length: 5 }).map((_entry, index) => (
              <TableRow key={index}><TableCell><Skeleton variant="text" sx={{ width: '60%' }} /></TableCell><TableCell><Skeleton variant="text" sx={{ width: '40%' }} /></TableCell><TableCell><Skeleton variant="text" sx={{ width: '30%' }} /></TableCell><TableCell /></TableRow>
            ))}</TableBody>
          </Table>
        </TableContainer>
      </Box>
    </Stack>
  ) : !listing ? (
    <Stack alignItems="center" justifyContent="center" sx={{ p: 4 }}>
      <Paper sx={{ ...surface, p: { xs: 3, sm: 4 }, maxWidth: 480 }}>
        <Stack spacing={2}>
          <Alert severity="error">{listingQuery.error instanceof ApiError ? t(`error.${listingQuery.error.code}`) : t('error.generic')}</Alert>
          <Stack direction="row" spacing={1}>
            <Button variant="contained" onClick={() => void listingQuery.refetch()}>{t('action.retry')}</Button>
            <Button onClick={() => window.location.reload()}>{t('workspace.refresh')}</Button>
          </Stack>
        </Stack>
      </Paper>
    </Stack>
  ) : (
    <Box sx={{ maxWidth: 1440, mx: 'auto', px: { xs: 2, sm: 3 }, py: { xs: 2, sm: 3 } }}>
      <Stack spacing={2.5}>
        <Box sx={{ display: 'flex', alignItems: 'stretch', flexDirection: 'column', gap: { xs: 1.5, sm: 2 }, '@media (min-width: 1024px)': { alignItems: 'center', flexDirection: 'row' } }}>
          <Box sx={{ flex: 1, minWidth: 0, alignSelf: 'stretch' }}>
            <Typography component="h1" variant="display" sx={{ fontSize: { xs: 20, sm: 24, lg: 28 }, lineHeight: { xs: '28px', sm: '32px', lg: '36px' }, whiteSpace: 'nowrap' }}>{t('app.workspace')}</Typography>
            <Breadcrumbs aria-label="breadcrumb" sx={{ mt: .5, overflow: 'hidden' }}>
              <Button component={Link} to={directoryRoute('')} size="small" sx={{ minWidth: 0, p: 0.5 }}>{t('workspace.root')}</Button>
              {breadcrumbs.map((segment, index, all) => (
                <Button component={Link} to={directoryRoute(all.slice(0, index + 1).join('/'))} size="small" key={`${segment}-${index}`} sx={{ minWidth: 0, p: 0.5 }}>{segment}</Button>
              ))}
            </Breadcrumbs>
            <Typography variant="caption" color="text.secondary" component="p" sx={{ mt: 0.75 }}>
              {listing.disk && listing.disk.totalBytes > 0
                ? t('storage.volume', { free: formatBytes(listing.disk.freeBytes), total: formatBytes(listing.disk.totalBytes) })
                : t('storage.unknown')}
            </Typography>
          </Box>
          {!mutable && canUpload && <Chip label={t('workspace.uploadsOnly')} color="info" variant="outlined" />}
          {!mutable && !canUpload && <Chip label={t('workspace.readOnly')} variant="outlined" />}
          <Box sx={{ display: { xs: 'grid', sm: 'flex' }, gridTemplateColumns: { xs: 'repeat(2, minmax(0, 1fr))', sm: undefined }, flexWrap: 'wrap', gap: 1, width: { xs: '100%', sm: 'auto' } }}>
            <Button
              size={mobile ? 'small' : 'medium'}
              sx={{ minWidth: 0 }}
              startIcon={refreshInProgress ? <CircularProgress color="inherit" size={18} /> : <Refresh />}
              variant="outlined"
              disabled={refreshInProgress}
              onClick={() => void handleManualRefresh()}
            >
              {refreshInProgress ? t('workspace.refreshing') : t('workspace.refresh')}
            </Button>
            <Button size={mobile ? 'small' : 'medium'} sx={{ minWidth: 0, minHeight: 44 }} startIcon={<StarBorderOutlined />} variant="outlined" onClick={() => setFavoritesOpen(true)}>{t('favorites.title')}</Button>
            {selectedEntries.length === 0 && <FavoriteButton directory={currentPath} />}
            {mutable && <Button size={mobile ? 'small' : 'medium'} sx={{ minWidth: 0 }} startIcon={<CreateNewFolder />} variant="outlined" onClick={() => setForm({ action: 'newdir' })}>{t('workspace.newFolder')}</Button>}
            {mutable && <Button size={mobile ? 'small' : 'medium'} sx={{ minWidth: 0 }} startIcon={<Add />} variant="outlined" onClick={() => setForm({ action: 'newfile' })}>{t('workspace.newFile')}</Button>}
            {canUpload && <Button size={mobile ? 'small' : 'medium'} sx={{ minWidth: 0 }} startIcon={<UploadFile />} variant="contained" onClick={() => setShowUploads(true)}>{t('workspace.upload')}</Button>}
          </Box>
        </Box>
        {refreshError && <Alert severity="warning" action={<Button color="inherit" size="small" disabled={refreshInProgress} onClick={() => void handleManualRefresh()}>{t('action.retry')}</Button>}>{refreshError}</Alert>}
        {manualRefresh && <Typography role="status" aria-live="polite" sx={{ position: 'absolute', width: 1, height: 1, p: 0, m: -1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap', border: 0 }}>{t('workspace.refreshing')}</Typography>}
        {listing.truncated && <Alert severity="warning"><Stack spacing={0.5}><span>{t('workspace.truncated')}</span><span>{t('workspace.truncatedFiltering')}</span></Stack></Alert>}
        <Paper sx={{ ...surface, p: { xs: 1.25, sm: 1.5 } }}>
          <Stack direction={{ xs: 'column', md: 'row' }} spacing={1.25}>
            <TextField
              fullWidth
              size="small"
              label={t('workspace.search')}
              placeholder={t('workspace.searchPlaceholder')}
              value={searchQuery}
              onChange={(event) => updateListingControls(() => setSearchQuery(event.target.value))}
              slotProps={{ htmlInput: { type: 'search' } }}
            />
            <Button sx={{ flexShrink: 0, minHeight: 44 }} onClick={() => setSearchOpen(true)}>{t('searchPanel.title')}</Button>
            <TextField
              select
              size="small"
              label={t('workspace.filter')}
              value={kindFilter}
              onChange={(event) => updateListingControls(() => setKindFilter(event.target.value as ListingKindFilter))}
              sx={{ minWidth: { md: 164 } }}
            >
              <MenuItem value="all">{t('workspace.filterAll')}</MenuItem>
              <MenuItem value="file">{t('workspace.filterFiles')}</MenuItem>
              <MenuItem value="directory">{t('workspace.filterFolders')}</MenuItem>
              <MenuItem value="archive">{t('workspace.filterArchives')}</MenuItem>
            </TextField>
            <TextField
              select
              size="small"
              label={t('workspace.sort')}
              value={listingSort}
              onChange={(event) => updateListingControls(() => setListingSort(event.target.value as ListingSort))}
              sx={{ minWidth: { md: 216 } }}
            >
              <MenuItem value="folders-first">{t('workspace.sortFoldersFirst')}</MenuItem>
              <MenuItem value="name-asc">{t('workspace.sortNameAsc')}</MenuItem>
              <MenuItem value="name-desc">{t('workspace.sortNameDesc')}</MenuItem>
              <MenuItem value="modified-desc">{t('workspace.sortModifiedDesc')}</MenuItem>
              <MenuItem value="modified-asc">{t('workspace.sortModifiedAsc')}</MenuItem>
              <MenuItem value="size-desc">{t('workspace.sortSizeDesc')}</MenuItem>
              <MenuItem value="size-asc">{t('workspace.sortSizeAsc')}</MenuItem>
            </TextField>
          </Stack>
        </Paper>
        {selectedEntries.length > 0 && <Paper component="section" aria-label={t('workspace.selected', { count: selectedEntries.length })} sx={{ ...surface, p: 1.25 }}>
          <Stack spacing={1.25}>
            <Stack direction="row" alignItems="center" justifyContent="space-between" spacing={1}>
              <Typography variant="bodyStrong">{t('workspace.selected', { count: selectedEntries.length })}</Typography>
              <Button size="small" variant="text" color="inherit" startIcon={<Close />} sx={{ color: 'text.secondary', flexShrink: 0, minWidth: 0, minHeight: { xs: 44, sm: 32 }, '&:hover': { bgcolor: 'action.hover', boxShadow: 'none' } }} onClick={() => setSelected(new Set())}>{t('action.clearSelection')}</Button>
            </Stack>
            <Box sx={{ display: { xs: 'grid', sm: 'flex' }, gridTemplateColumns: { xs: 'repeat(2, minmax(0, 1fr))', sm: undefined }, flexWrap: 'wrap', gap: 0.75 }}>
              {selectedEntries.length === 1 && selectedEntries[0].kind === 'directory' && <FavoriteButton directory={selectedEntries[0].path} selected />}
              {mutable && <Button size="small" sx={{ minWidth: 0 }} startIcon={<Folder />} onClick={() => setForm({ action: 'move' })}>{t('action.move')}</Button>}
              {mutable && <Button size="small" sx={{ minWidth: 0 }} startIcon={<ContentCopy />} onClick={() => setForm({ action: 'copy' })}>{t('action.copy')}</Button>}
              {mutable && <Button size="small" sx={{ minWidth: 0 }} startIcon={<DeleteOutline />} onClick={() => setPendingDelete({ type: 'batch', entries: selectedEntries })}>{t('action.moveToRecycleBin')}</Button>}
              <Button size="small" sx={{ minWidth: 0 }} startIcon={<Archive />} onClick={() => doBatch('do/batch/download-zip')}>{t('action.batchDownload')}</Button>
            </Box>
          </Stack>
        </Paper>}
        {mobile ? <MobileFileList
          parentPath={listing.parentPath}
          entries={displayedEntries}
          selected={selectedForCurrentToken}
          selectionState={selectionState}
          mutable={mutable}
          editorAvailable={Boolean(session?.capabilities.editorSave || session?.capabilities.browse)}
          busy={refreshInProgress}
          onSelect={select}
          onSelectAll={(checked) => setSelected(checked ? new Set(displayedEntries.map((entry) => entry.path)) : new Set())}
          onAction={performEntryAction}
          onMenu={(entry, anchor) => { setMenuEntry(entry); setMenuAnchor(anchor); }}
          emptyState={entries.length === 0 ? <EmptyState
            icon={<FolderOffOutlined />}
            title={t('workspace.emptyTitle')}
            caption={t('workspace.emptyHint')}
            action={canUpload ? <Button variant="contained" onClick={() => setShowUploads(true)}>{t('workspace.upload')}</Button> : undefined}
          /> : <EmptyState
            icon={<FolderOffOutlined />}
            title={t('workspace.noMatchesTitle')}
            caption={t('workspace.noMatchesHint')}
            action={<Button onClick={() => updateListingControls(() => { setSearchQuery(''); setKindFilter('all'); })}>{t('workspace.clearFilters')}</Button>}
          />}
        /> : <TableContainer component={Paper} aria-busy={refreshInProgress || undefined} sx={surface}>
          <Table stickyHeader size={compact ? 'small' : 'medium'} aria-label={t('app.workspace')}>
            <TableHead><TableRow>
              <TableCell padding="checkbox"><Checkbox aria-label={t('workspace.selectAll')} checked={selectionState.allSelected} indeterminate={selectionState.partiallySelected} onChange={(event) => setSelected(event.target.checked ? new Set(displayedEntries.map((entry) => entry.path)) : new Set())} /></TableCell>
              <TableCell sx={desktopTableColumnSx.name}>{t('workspace.name')}</TableCell>
              {!compact && <TableCell sx={desktopTableColumnSx.size}>{t('workspace.size')}</TableCell>}
              {!compact && <TableCell sx={desktopTableColumnSx.modified}>{t('workspace.modified')}</TableCell>}
              <TableCell align="right" sx={{ width: 160 }}>{t('workspace.actions')}</TableCell>
            </TableRow></TableHead>
            <TableBody>
              {listing.parentPath !== null && <TableRow hover>
                <TableCell colSpan={compact ? 3 : 5}>
                  <Button component={Link} to={directoryRoute(listing.parentPath)} aria-label={t('workspace.parentDirectory')} title={t('workspace.parentDirectory')} startIcon={<ArrowUpward fontSize="small" />} color="inherit" sx={{ minHeight: 44, justifyContent: 'flex-start', width: '100%', fontWeight: 700 }}>{t('workspace.upOneLevel')}</Button>
                </TableCell>
              </TableRow>}
              {displayedEntries.map((entry) => (
                <TableRow hover key={entry.path} selected={selectedForCurrentToken.has(entry.path)}>
                  <TableCell padding="checkbox"><Checkbox aria-label={t('workspace.selectItem', { name: entry.name })} checked={selectedForCurrentToken.has(entry.path)} onChange={(event) => select(entry, event.target.checked)} /></TableCell>
                  <TableCell sx={{ maxWidth: 0 }}>
                    <Stack direction="row" spacing={1.25} alignItems="center">
                      <ListItemIcon sx={{ minWidth: 28, color: entry.kind === 'directory' ? 'primary.light' : 'text.secondary' }}>
                        {entry.kind === 'directory' ? <Folder /> : <DescriptionOutlined />}
                      </ListItemIcon>
                      <Box sx={{ minWidth: 0 }}>
                        <Typography
                          component={entry.kind === 'directory' ? Link : 'button'}
                          to={entry.kind === 'directory' ? directoryRoute(entry.path) : undefined}
                          type={entry.kind === 'directory' ? undefined : 'button'}
                          onClick={entry.kind === 'file' ? () => window.location.assign(itemUrl('download', entry.path)) : undefined}
                          color="inherit"
                          fontWeight={entry.kind === 'directory' ? 700 : 500}
                          sx={fileNameButtonSx}
                        >
                          {entry.name}
                        </Typography>
                        {compact && <Typography variant="caption" color="text.secondary" component="div">{entry.kind === 'file' ? formatBytes(entry.sizeBytes) : entryKindLabel(entry.kind, t)} · {fmtDate(entry.modifiedAt)}</Typography>}
                      </Box>
                    </Stack>
                  </TableCell>
                  {!compact && <TableCell sx={desktopTableColumnSx.size}>{entry.kind === 'file' ? formatBytes(entry.sizeBytes) : '—'}</TableCell>}
                  {!compact && <TableCell sx={desktopTableColumnSx.modified}>{fmtDate(entry.modifiedAt)}</TableCell>}
                  <TableCell align="right" sx={{ width: 160 }}>
                    <RowActions
                      entry={entry}
                      mutable={mutable}
                      editorAvailable={Boolean(session?.capabilities.editorSave || session?.capabilities.browse)}
                      onAction={(name) => performEntryAction(name, entry)}
                      onMenu={(event) => { setMenuEntry(entry); setMenuAnchor(event.currentTarget); }}
                    />
                  </TableCell>
                </TableRow>
              ))}
              {entries.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={compact ? 3 : 5}>
                    <EmptyState
                      icon={<FolderOffOutlined />}
                      title={t('workspace.emptyTitle')}
                      caption={t('workspace.emptyHint')}
                      action={canUpload ? <Button variant="contained" onClick={() => setShowUploads(true)}>{t('workspace.upload')}</Button> : undefined}
                    />
                  </TableCell>
                </TableRow>
              ) : displayedEntries.length === 0 && (
                <TableRow>
                  <TableCell colSpan={compact ? 3 : 5}>
                    <EmptyState
                      icon={<FolderOffOutlined />}
                      title={t('workspace.noMatchesTitle')}
                      caption={t('workspace.noMatchesHint')}
                      action={<Button onClick={() => updateListingControls(() => { setSearchQuery(''); setKindFilter('all'); })}>{t('workspace.clearFilters')}</Button>}
                    />
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </TableContainer>}
      </Stack>
    </Box>
  );

  return <Box component="main" sx={{ minHeight: '100dvh' }}>
    <AppBar position="sticky" elevation={0} color="transparent" sx={{ borderBottom: '1px solid', borderColor: 'divider', bgcolor: 'var(--mui-palette-AppBar-defaultBg)' }}>
      <Toolbar sx={{ gap: { xs: .5, sm: 1 }, px: { xs: 1, sm: 3 } }}>
        <Box sx={{ lineHeight: 0 }}><Mark size={22} /></Box>
        <Typography variant="bodyStrong" sx={{ mr: 'auto', display: { xs: 'none', sm: 'block' } }}>FileHarbor</Typography>
        <Button variant="outlined" startIcon={<TaskAlt />} onClick={() => tasks?.open()} sx={{ ml: { xs: 'auto', sm: 0 }, minHeight: 44, flexShrink: 0, bgcolor: tasks?.activeCount ? 'action.selected' : undefined }}>
          {t('tasks.title')}
          {tasks?.failed ? <Box component="span" sx={{ ml: 1 }} aria-label={t('error.job_unavailable')}>!</Box> : tasks?.loading ? <CircularProgress size={14} sx={{ ml: 1 }} /> : Boolean(tasks?.activeCount) && <Box component="span" sx={{ ml: 1, px: .75, borderRadius: 1, bgcolor: 'primary.main', color: 'primary.contrastText', fontVariantNumeric: 'tabular-nums' }}>{tasks?.activeCount}</Box>}
        </Button>
        <Tooltip title={t('recycleBin.title')}>
          <IconButton aria-label={t('recycleBin.title')} onClick={() => setRecycleBinOpen(true)}><DeleteOutline /></IconButton>
        </Tooltip>
        <Tooltip title={t('app.settings')}>
          <IconButton aria-label={t('app.settings')} onClick={() => setSettingsOpen(true)}><SettingsOutlined /></IconButton>
        </Tooltip>
        <Tooltip title={t('app.signOut')}>
          <IconButton aria-label={t('app.signOut')} onClick={() => void signOut()}><LogoutOutlined /></IconButton>
        </Tooltip>
      </Toolbar>
    </AppBar>
    {listingContent}
    <SettingsPanel open={settingsOpen} onClose={() => setSettingsOpen(false)} />
    {listing && <>
    <RecycleBinPanel
      open={recycleBinOpen}
      onClose={() => setRecycleBinOpen(false)}
      mutable={mutable}
      currentDirectory={currentPath}
      onRestored={refreshDirectory}
      onNotice={(message, severity) => setNotice({ message, severity })}
    />
    <EntryMenu entry={menuEntry} anchor={menuAnchor} onClose={() => { setMenuEntry(null); setMenuAnchor(null); }} onAction={performEntryAction} mutable={mutable} editorAvailable={Boolean(session?.capabilities.editorSave || session?.capabilities.browse)} />
    <EntryForm state={form} currentPath={listing.path} selectedEntries={selectedEntries} onClose={() => setForm(null)} onSubmit={(endpoint, values) => {
      if (endpoint === 'do/copy' && tasks) {
        const entry = listing.entries.find(item => item.path === values.path);
        if (entry) void tasks.submit({ kind: 'copy', path: entry.path, version: entry.version, destination: values.destination, name: values.name }).catch(error => setNotice({ message: error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic'), severity: 'error' }));
      } else mutation.mutate({ endpoint, values, affectedDirectories: affectedDirectories(values.destination) });
    }} onBatchSubmit={doBatch} />
    {favoritesOpen && <FavoritesPanel key={currentPath} directory={currentPath} onClose={() => setFavoritesOpen(false)} onNavigate={(path) => { setFavoritesOpen(false); navigate(directoryRoute(path)); }} />}
    {searchOpen && <SearchPanel key={currentPath} directory={currentPath} onClose={() => setSearchOpen(false)} onNavigate={(path) => { setSearchOpen(false); navigate(directoryRoute(path)); }} />}
    {extractFor && <ExtractArchiveDialog key={extractFor.path} entry={extractFor} onClose={() => setExtractFor(null)} />}
    <ArchivePreviewPanel entry={archivePreviewFor} onClose={() => setArchivePreviewFor(null)} />
    <PropertiesDialog entry={propertiesFor} properties={propertyQuery.data?.properties} isLoading={propertyQuery.isLoading} onClose={() => setPropertiesFor(null)} />
    {activeEditor && <Suspense fallback={<Stack role="status" aria-live="polite" alignItems="center" justifyContent="center" sx={{ minHeight: 200 }}><CircularProgress /><Typography variant="caption" color="text.secondary" sx={{ mt: 2 }}>{t('editor.loading')}</Typography></Stack>}><LazyEditorDialog key={activeEditor.path} entry={activeEditor} writable={Boolean(session?.capabilities.editorSave)} onClose={closeEditor} onSaved={() => refreshDirectory(editorReturnPath)} /></Suspense>}
    {canUpload && <UploadQueueDrawer open={showUploads} onClose={() => setShowUploads(false)} destination={listing.path} username={session?.username ?? ''} onAllComplete={(paths) => { for (const path of paths) void refreshDirectory(path); }} />}
    <DialogShell
      open={Boolean(pendingDelete)}
      onClose={() => setPendingDelete(null)}
      title={pendingDelete?.type === 'batch'
        ? t('dialog.confirmMoveToRecycleBinBatch', { count: pendingDelete.entries.length })
        : t('dialog.confirmMoveToRecycleBin', { name: pendingDelete?.type === 'single' ? pendingDelete.entry.name : '' })}
      confirmLabel={t('action.moveToRecycleBin')}
      onConfirm={confirmDelete}
    >
      <Typography color="text.secondary">{t('dialog.moveToRecycleBinText')}</Typography>
    </DialogShell>
    <Snackbar open={Boolean(notice)} autoHideDuration={6000} onClose={() => setNotice(null)}><Alert severity={notice?.severity ?? 'info'} variant="filled" onClose={() => setNotice(null)}>{notice?.message}</Alert></Snackbar>
    </>}
  </Box>;
}

export function EntryMenu({ entry, anchor, onClose, onAction, mutable, editorAvailable }: { entry: FileEntry | null; anchor: HTMLElement | null; onClose: () => void; onAction: (action: EntryActionName, entry: FileEntry) => void; mutable: boolean; editorAvailable: boolean }) {
  const { t } = useI18n();
  if (!entry) return <Menu anchorEl={anchor} open={false} onClose={onClose} />;
  const visible = entryMenuActions(entry, mutable, editorAvailable);
  const primary = visible.filter((item) => item.name !== 'trash');
  const destructive = visible.filter((item) => item.name === 'trash');
  const render = (item: EntryAction) => {
    const Icon = item.icon;
    return (
      <MenuItem key={item.name} onClick={() => onAction(item.name, entry)}>
        <ListItemIcon><Icon fontSize="small" /></ListItemIcon>
        {t(`action.${item.name}`)}
      </MenuItem>
    );
  };
  return (
    <Menu anchorEl={anchor} open={Boolean(anchor)} onClose={onClose}>
      {primary.map(render)}
      {primary.length > 0 && destructive.length > 0 && <Divider />}
      {destructive.map(render)}
    </Menu>
  );
}

function EntryForm({ state, currentPath, selectedEntries, onClose, onSubmit, onBatchSubmit }: { state: FormState; currentPath: string; selectedEntries: FileEntry[]; onClose: () => void; onSubmit: (endpoint: string, values: Record<string, string>) => void; onBatchSubmit: (endpoint: string, destination?: string) => void }) {
  const theme = useTheme();
  const { t } = useI18n();
  const [value, setValue] = useState('');
  const [destination, setDestination] = useState(currentPath);
  const fullScreenDestinationPicker = useMediaQuery(theme.breakpoints.down('sm'));

  useEffect(() => {
    if (state) {
      setValue(state.entry?.name ?? '');
      setDestination(currentPath);
    }
  }, [state, currentPath]);

  if (!state) return null;
  const title: Record<FormAction, string> = { newdir: t('dialog.newFolder'), newfile: t('dialog.newFile'), rename: t('dialog.rename'), move: t('action.move'), copy: t('action.copy') };
  const isDestinationAction = state.action === 'move' || state.action === 'copy';
  const isMultiple = !state.entry && isDestinationAction;
  const destinationIsSource = isDestinationAction && destination === currentPath;
  const submit = () => {
    if (state.action === 'newdir') onSubmit('do/newdir', { path: currentPath, dirname: value });
    else if (state.action === 'newfile') onSubmit('do/newfile', { path: currentPath, filename: value });
    else if (state.action === 'rename' && state.entry) onSubmit('do/rename', { path: state.entry.path, name: value });
    else if (isMultiple) onBatchSubmit(`do/batch/${state.action}`, destination);
    else if (state.entry) onSubmit(`do/${state.action}`, { path: state.entry.path, destination, ...(value && value !== state.entry.name ? { name: value } : {}) });
    onClose();
  };

  return (
    <DialogShell
      open
      onClose={onClose}
      title={title[state.action]}
      maxWidth={isDestinationAction ? 'sm' : 'xs'}
      fullScreen={isDestinationAction && fullScreenDestinationPicker}
      onConfirm={submit}
      confirmLabel={isDestinationAction ? t(`action.${state.action}`) : undefined}
      confirmDisabled={(!isMultiple && !value.trim()) || destinationIsSource}
      contentSx={isDestinationAction ? { display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1 } : undefined}
      stackSx={isDestinationAction ? { minHeight: 0, flex: 1 } : undefined}
      actionsSx={isDestinationAction ? { flexDirection: { xs: 'column-reverse', sm: 'row' }, gap: 1, '& > .MuiButton-root': { width: { xs: '100%', sm: 'auto' } } } : undefined}
    >
      {!isMultiple && <TextField autoFocus fullWidth required label={state.action === 'newdir' ? t('dialog.folderName') : state.action === 'newfile' ? t('dialog.fileName') : t('dialog.newName')} value={value} onChange={(event) => setValue(event.target.value)} />}
      {isMultiple && <Typography color="text.secondary">{t('workspace.selected', { count: selectedEntries.length })}</Typography>}
      {isDestinationAction && <FolderDestinationPicker value={destination} onChange={setDestination} />}
      {destinationIsSource && <Alert severity="info">{t('error.destination_same_directory')}</Alert>}
    </DialogShell>
  );
}

function DefinitionRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <Stack direction="row" justifyContent="space-between" alignItems="baseline" sx={{ py: 1, borderBottom: '1px solid', borderColor: 'divider' }} spacing={2}>
      <Typography variant="caption" color="text.secondary" sx={{ flexShrink: 0 }}>{label}</Typography>
      <Typography variant="body" sx={{ fontFamily: mono ? fontFamilyMono : undefined, overflowWrap: 'anywhere', textAlign: 'right', minWidth: 0 }}>{value || '—'}</Typography>
    </Stack>
  );
}

function PropertiesDialog({ entry, properties, isLoading, onClose }: { entry: FileEntry | null; properties?: Properties; isLoading: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const titleText = t('properties.title');
  return (
    <SidePanel open={Boolean(entry)} onClose={onClose} icon={<InfoOutlined />} title={titleText}>
      {isLoading && <Stack alignItems="center" justifyContent="center" sx={{ minHeight: 200 }}><CircularProgress /></Stack>}
      {properties && <Stack spacing={0} sx={{ mt: -1 }}>
        <DefinitionRow label={t('properties.type')} value={entryKindLabel(properties.kind, t)} />
        <DefinitionRow label={t('properties.location')} value={properties.path} mono />
        {properties.kind === 'directory' ? <DirectorySize key={properties.path} path={properties.path} /> : <DefinitionRow label={t('properties.size')} value={formatBytes(properties.size)} />}
        <DefinitionRow label={t('properties.modified')} value={fmtDate(new Date(properties.modified * 1000).toISOString())} />
        <DefinitionRow label={t('properties.permissions')} value={properties.mode} mono />
        {properties.entry_count !== undefined && <DefinitionRow label={t('properties.contents')} value={properties.entry_count.toString()} />}
        {properties.incomplete && <Alert severity="info" sx={{ mt: 2 }}>{t('properties.incomplete')}</Alert>}
      </Stack>}
    </SidePanel>
  );
}