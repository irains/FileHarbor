import { useEffect, useMemo, useRef, useState } from 'react';
import { Add, CancelOutlined, DeleteOutline, Pause, PlayArrow, UploadFile } from '@mui/icons-material';
import { Alert, Box, Button, IconButton, LinearProgress, List, ListItem, ListItemIcon, ListItemText, Stack, Typography } from '@mui/material';
import { api, ApiError } from '../api/client';
import { filesWithDirectories, droppedDirectories, pickDirectory, supportsDirectoryPicker, type DirectorySelection } from '../uploads/directoryEntries';
import { getRuntime } from '../runtime';
import { useI18n } from '../i18n';
import { ReliableUploadQueue, type QueueItem } from '../uploads/queue';
import { listStoredUploads, uploadScope } from '../uploads/storage';
import { radii } from '../tokens';
import { SidePanel } from './SidePanel';
import { EmptyState } from './EmptyState';

function labelFor(phase: QueueItem['phase'], t: (key: string) => string) {
  return phase === 'reselect' ? t('upload.resumeHint') : t(`upload.${phase === 'failed' ? 'failed' : phase}`);
}

export function UploadQueueDrawer({ open, onClose, destination, username, onAllComplete }: { open: boolean; onClose: () => void; destination: string; username: string; onAllComplete: (destinationPaths: string[]) => void }) {
  const { t } = useI18n();
  const runtime = getRuntime();
  const enumeration = useRef<AbortController | null>(null);
  useEffect(() => () => enumeration.current?.abort(), []);
  const directoryInput = useRef<HTMLInputElement>(null);
  const [preparing, setPreparing] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const destinationRef = useRef(destination);
  destinationRef.current = destination;
  const [items, setItems] = useState<QueueItem[]>([]);
  const [attachmentID, setAttachmentID] = useState<string | null>(null);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);
  const [hasPersisted, setHasPersisted] = useState<boolean>(false);
  const queue = useMemo(() => new ReliableUploadQueue(uploadScope(window.location.origin, runtime.basePath, username), () => destinationRef.current), [runtime.basePath, username]);
  useEffect(() => {
    const unsubscribe = queue.subscribe(() => setItems(queue.snapshot()));
    return unsubscribe;
  }, [queue]);
  useEffect(() => { void listStoredUploads(uploadScope(window.location.origin, runtime.basePath, username)).then((saved) => { setHasPersisted(saved.length > 0); queue.restore(saved); }); }, [queue, runtime.basePath, username]);
  const completedDestinations = useRef(new Set<string>());
  const wasAllDone = useRef(false);
  const hasNonTerminal = items.some((item) => !['completed', 'cancelled'].includes(item.phase));
  if (wasAllDone.current && hasNonTerminal) {
    completedDestinations.current.clear();
    wasAllDone.current = false;
  }
  for (const item of items) if (item.phase === 'completed') completedDestinations.current.add(item.path);
  const allDone = items.length > 0 && completedDestinations.current.size > 0 && !hasNonTerminal;
  useEffect(() => {
    if (allDone && !wasAllDone.current) {
      onAllComplete([...completedDestinations.current]);
      wasAllDone.current = true;
    }
  }, [allDone, onAllComplete]);
  const openFilePicker = (id?: string) => {
    setAttachmentID(id ?? null);
    setAttachmentError(null);
    fileInput.current?.click();
  };
  const handleFiles = (files: FileList | null) => {
    if (!files?.length) return;
    if (attachmentID && files.length === 1) {
      const id = attachmentID;
      setAttachmentID(null);
      void queue.attachFile(id, files[0]).then((attached) => { if (!attached) setAttachmentError(t('upload.sourceChanged')); });
      return;
    }
    setAttachmentID(null);
    queue.add([...files]);
  };
  const addDirectorySelection = async (selection: DirectorySelection, target: string) => {
    setPreparing(true); setAttachmentError(null);
    try {
      if (selection.directories.length) await api.prepareUploadDirectories(target, selection.directories);
      if (!await queue.restoreDirectoryFiles(selection.files, target)) setAttachmentError(t('upload.sourceChanged'));
      if (!selection.files.length) onAllComplete([target]);
    } catch (error) { setAttachmentError(error instanceof ApiError ? t(`error.${error.code}`) : t('error.generic')); }
    finally { setPreparing(false); }
  };
  const chooseDirectory = async () => {
    if (!supportsDirectoryPicker()) { directoryInput.current?.click(); return; }
    if (enumeration.current) return;
    const controller = new AbortController();
    enumeration.current = controller;
    const target = destinationRef.current;
    setPreparing(true); setAttachmentError(null);
    try {
      const selection = await pickDirectory(controller.signal);
      controller.signal.throwIfAborted();
      await addDirectorySelection(selection, target);
    } catch (error) {
      if (!controller.signal.aborted && !(error instanceof DOMException && error.name === 'AbortError')) setAttachmentError(t('error.generic'));
    } finally { enumeration.current = null; setPreparing(false); }
  };
  const selectFiles = () => openFilePicker();
  return (
    <SidePanel open={open} onClose={onClose} icon={<UploadFile />} title={t('upload.title')}>
      <Stack spacing={1.5} sx={{ height: '100%' }}>
        <input ref={directoryInput} type="file" hidden multiple {...{ webkitdirectory: '' }} onChange={(event) => {
          const files = Array.from(event.target.files || []);
          event.currentTarget.value = '';
          try { void addDirectorySelection(filesWithDirectories(files), destinationRef.current); }
          catch { setAttachmentError(t('error.invalid_path')); }
        }} />
        <Button variant="outlined" disabled={preparing} onClick={() => void chooseDirectory()}>{t('folderUpload.choose')}</Button>
        <Typography variant="caption" color="text.secondary">{t('folderUpload.hint')}</Typography>
        {preparing && <LinearProgress aria-label={t('folderUpload.preparing')} />}
        {preparing && enumeration.current && <Button onClick={() => enumeration.current?.abort()}>{t('action.cancel')}</Button>}
        <input ref={fileInput} type="file" hidden multiple={!attachmentID} onChange={(event) => { handleFiles(event.target.files); event.currentTarget.value = ''; }} />
        <Button variant="contained" startIcon={<Add />} onClick={selectFiles}>{t('upload.addFiles')}</Button>
        <Box
          role="button"
          tabIndex={0}
          aria-label={t('workspace.dropZone')}
          onClick={selectFiles}
          onKeyDown={(event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); selectFiles(); } }}
          onDragOver={(event) => event.preventDefault()}
          onDrop={(event) => { event.preventDefault(); if (preparing) return; setAttachmentID(null); const target = destinationRef.current; if (!event.dataTransfer.items?.length) { handleFiles(event.dataTransfer.files); return; } setPreparing(true); void droppedDirectories(event.dataTransfer.items).then(selection => addDirectorySelection(selection, target)).catch(() => setAttachmentError(t('error.invalid_path'))).finally(() => setPreparing(false)); }}
          sx={{ border: '1px dashed', borderColor: 'divider', borderRadius: radii.sm, px: 2, py: 1.5, color: 'text.secondary', cursor: 'pointer', textAlign: 'center', '&:hover': { borderColor: 'primary.main', bgcolor: 'action.hover' } }}
        >
          <Typography variant="caption">{t('workspace.dropZone')}</Typography>
        </Box>
        {attachmentError && <Alert severity="warning">{attachmentError}</Alert>}
        {allDone && <Alert severity="success" role="status">{t('upload.allDone')}</Alert>}
        {!items.length && !hasPersisted && (
          <EmptyState
            icon={<UploadFile />}
            title={t('upload.addFiles')}
            caption={t('workspace.dropFiles')}
            action={<Button variant="contained" startIcon={<Add />} onClick={selectFiles}>{t('upload.addFiles')}</Button>}
          />
        )}
        {!items.length && hasPersisted && <Alert severity="info">{t('upload.resumeHint')}</Alert>}
        <List sx={{ overflow: 'auto', mx: -1 }}>
          {items.map((item) => <UploadRow item={item} key={item.id} queue={queue} t={t} onAttach={() => openFilePicker(item.id)} />)}
        </List>
      </Stack>
    </SidePanel>
  );
}

function UploadRow({ item, queue, t, onAttach }: { item: QueueItem; queue: ReliableUploadQueue; t: (key: string, values?: Record<string, string | number>) => string; onAttach: () => void }) {
  const percent = Math.round(Math.min(100, item.progress * 100));
  const state = item.error ?? labelFor(item.phase, t);
  const resumeAction = item.phase === 'uploading' || item.phase === 'hashing' ? <IconButton aria-label={`${t('action.pause')} ${item.name}`} onClick={() => queue.pause(item.id)}><Pause /></IconButton>
    : ['paused', 'failed'].includes(item.phase) && item.file ? <IconButton aria-label={`${t('action.resume')} ${item.name}`} onClick={() => queue.resume(item.id)}><PlayArrow /></IconButton>
    : ['paused', 'failed', 'reselect'].includes(item.phase) ? <Button size="small" onClick={onAttach}>{t('upload.chooseOriginal')}</Button> : null;
  const terminal = ['completed', 'cancelled'].includes(item.phase);
  return (
    <ListItem
      alignItems="flex-start"
      secondaryAction={
        <Stack direction="row" spacing={0.5}>
          {resumeAction}
          {!terminal && <IconButton aria-label={`${t('action.cancelUpload')} ${item.name}`} onClick={() => void queue.cancel(item.id)}><CancelOutlined /></IconButton>}
          <IconButton aria-label={`${t('action.remove')} ${item.name}`} onClick={() => void queue.remove(item.id)}><DeleteOutline /></IconButton>
        </Stack>
      }
      sx={{ px: 1, py: 1.25 }}
    >
      <ListItemIcon sx={{ minWidth: 38 }}>
        <UploadFile color={item.phase === 'failed' ? 'error' : item.phase === 'completed' ? 'success' : 'action'} />
      </ListItemIcon>
      <ListItemText
        primary={<Typography title={[item.path, item.name].filter(Boolean).join('/')} sx={{ overflowWrap: 'anywhere' }}>{item.relativePath || item.name}</Typography>}
        secondaryTypographyProps={{ component: 'div' }}
        secondary={
          <Stack spacing={0.5}>
            <Typography variant="caption" color={item.phase === 'failed' ? 'error.main' : 'text.secondary'}>{state}</Typography>
            <LinearProgress aria-label={t('upload.progress', { name: item.name })} aria-valuetext={t('upload.status', { name: item.name, state, percent })} variant="determinate" value={percent} />
          </Stack>
        }
      />
    </ListItem>
  );
}
