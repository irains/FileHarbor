import { useState } from 'react';
import { Alert, FormControlLabel, Radio, RadioGroup, TextField, Typography } from '@mui/material';
import { type FileEntry, ApiError } from '../api/client';
import { useTasks } from '../tasks/TaskProvider';
import { useI18n } from '../i18n';
import { DialogShell } from './DialogShell';
import { FolderDestinationPicker } from './FolderDestinationPicker';

export function ExtractArchiveDialog({ entry, onClose }: { entry: FileEntry; onClose: () => void }) {
  const { t } = useI18n();
  const tasks = useTasks();
  const [mode, setMode] = useState('new_folder');
  const [name, setName] = useState(entry.name.replace(/\.(tar\.(snappy|bzip2|zstd|zlib|gzip|gz|bz2|xz|zst|lz4|br|sz|zz)|snappy|bzip2|gzip|zstd|zlib|tgz|tbz2|tbz|tzst|txz|zip|rar|tar|gz|bz2|xz|zst|lz4|br|sz|zz)$/i, ''));
  const parent = entry.path.split('/').slice(0, -1).join('/');
  const [directory, setDirectory] = useState(parent);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const output = mode === 'new_folder' ? [parent, name].filter(Boolean).join('/') : mode === 'chosen' ? directory : parent;
  const submit = async () => {
    setPending(true); setError('');
    try {
      if (!tasks) throw new Error('tasks unavailable');
      await tasks.submit({ kind: 'extract', path: entry.path, version: entry.version, target: { mode, ...(mode === 'new_folder' ? { name } : mode === 'chosen' ? { directory } : {}) } });
      onClose();
    } catch (failure) { setError(failure instanceof ApiError ? failure.code : 'generic'); }
    finally { setPending(false); }
  };
  return <DialogShell open onClose={onClose} title={t('action.extract')} maxWidth="sm" onConfirm={() => void submit()} confirmDisabled={pending || (mode === 'new_folder' && !name.trim())}>
    <RadioGroup value={mode} onChange={(_, value) => setMode(value)}>
      {['new_folder', 'current', 'chosen'].map(value => <FormControlLabel key={value} value={value} control={<Radio />} label={t(`extractTarget.${value}`)} />)}
    </RadioGroup>
    {mode === 'new_folder' && <TextField label={t('dialog.folderName')} value={name} onChange={event => setName(event.target.value)} />}
    {mode === 'chosen' && <FolderDestinationPicker value={directory} onChange={setDirectory} />}
    <Typography sx={{ overflowWrap: 'anywhere' }}>{t('folderPicker.selected', { path: output || t('workspace.root') })}</Typography>
    <Alert severity="info">{t('extractTarget.hint')}</Alert>
    {error && <Alert severity="error">{t(`error.${error}`)}</Alert>}
  </DialogShell>;
}
