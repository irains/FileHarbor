import { useEffect, useRef, useState, type FormEvent } from 'react';
import { SearchOutlined } from '@mui/icons-material';
import { Alert, Box, Button, Checkbox, FormControlLabel, LinearProgress, List, ListItem, Stack, TextField, Typography } from '@mui/material';
import { api, ApiError, type SearchResult } from '../api/client';
import { formatBytes } from '../formatBytes';
import { useI18n } from '../i18n';
import { SidePanel } from './SidePanel';

export function SearchPanel({ directory, onClose, onNavigate }: { directory: string; onClose: () => void; onNavigate: (path: string) => void }) {
  const { t } = useI18n();
  const [recursive, setRecursive] = useState(true);
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => { active.current?.abort(); }, []);
  const cancel = () => {
    active.current?.abort();
    active.current = null;
    setBusy(false);
  };
  const search = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    const data = new FormData(event.currentTarget);
    const parameters: Record<string, string> = { path: directory, recursive: String(recursive) };
    setBusy(true); setError(null); setResult(null);
    try {
      for (const key of ['name', 'extension', 'min_size', 'max_size', 'modified_after', 'modified_before']) {
        const value = String(data.get(key) ?? '');
        if (value) parameters[key] = key.startsWith('modified_') ? new Date(value).toISOString() : value;
      }
      const response = await api.search(parameters, controller.signal);
      if (active.current === controller && !controller.signal.aborted) setResult(response.result);
    } catch (failure) {
      if (active.current === controller && !controller.signal.aborted) setError(failure instanceof ApiError ? failure.code : 'generic');
    } finally {
      if (active.current === controller) { active.current = null; setBusy(false); }
    }
  };
  return <SidePanel open onClose={onClose} icon={<SearchOutlined />} title={t('searchPanel.title')}>
    <Stack spacing={2}>
      <Typography variant="body2" sx={{ overflowWrap: 'anywhere' }}>{t('searchPanel.scope', { path: directory || t('workspace.root') })}</Typography>
      <Stack component="form" spacing={1.5} onSubmit={search}>
        <TextField name="name" label={t('searchPanel.name')} size="small" slotProps={{ htmlInput: { maxLength: 512 } }} />
        <TextField name="extension" label={t('searchPanel.extension')} size="small" slotProps={{ htmlInput: { maxLength: 128 } }} />
        <FormControlLabel control={<Checkbox checked={recursive} onChange={(_, checked) => setRecursive(checked)} />} label={t('searchPanel.recursive')} />
        <Box sx={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 1.5 }}>
          {['min_size', 'max_size'].map((key) => <TextField key={key} name={key} type="number" label={t(`searchPanel.${key}`)} size="small" slotProps={{ htmlInput: { min: 0, step: 1 } }} />)}
        </Box>
        {['modified_after', 'modified_before'].map((key) => <TextField key={key} name={key} type="datetime-local" label={t(`searchPanel.${key}`)} size="small" slotProps={{ inputLabel: { shrink: true }, htmlInput: { step: 1 } }} />)}
        <Typography variant="caption" color="text.secondary">{t('searchPanel.hint')}</Typography>
        <Stack direction="row" spacing={1}>
          <Button type="submit" variant="contained">{t('searchPanel.submit')}</Button>
          {busy && <Button onClick={cancel}>{t('action.cancel')}</Button>}
        </Stack>
      </Stack>
      {busy && <LinearProgress aria-label={t('searchPanel.searching')} />}
      {error && <Alert severity="error">{t(`error.${error}`)}</Alert>}
      {result && <>
        <Typography role="status" variant="body2">{t('searchPanel.count', { count: result.entries.length, visited: result.visited })}</Typography>
        {result.incomplete && <Alert severity="warning">{t('searchPanel.incomplete', { skipped: result.skipped })}</Alert>}
        {result.entries.length === 0 && <Typography>{t('searchPanel.empty')}</Typography>}
        <List disablePadding>
          {result.entries.map((entry) => <ListItem key={entry.path} disableGutters sx={{ display: 'block', py: 1.5, borderBottom: '1px solid', borderColor: 'divider' }}>
            <Typography variant="body2" sx={{ overflowWrap: 'anywhere', whiteSpace: 'pre-wrap' }}>{entry.path}</Typography>
            <Typography variant="caption" color="text.secondary">{entry.kind === 'file' ? formatBytes(entry.size) : t('workspace.folder')} · {new Date(entry.modified).toLocaleString()}</Typography>
            <Box><Button size="small" sx={{ minHeight: 44 }} onClick={() => onNavigate(entry.kind === 'directory' ? entry.path : entry.parent)}>{t(entry.kind === 'directory' ? 'searchPanel.open' : 'searchPanel.openParent')}</Button></Box>
          </ListItem>)}
        </List>
      </>}
    </Stack>
  </SidePanel>;
}
