import type { SvgIconComponent } from '@mui/icons-material';
import {
  ContentCopyOutlined,
  DeleteOutline,
  DownloadOutlined,
  DriveFileMoveOutlined,
  DriveFileRenameOutline,
  EditOutlined,
  Fingerprint,
  FolderZipOutlined,
  InfoOutlined,
  UnarchiveOutlined,
  VisibilityOutlined
} from '@mui/icons-material';
import type { FileEntry } from '../api/client';

export type EntryActionName =
  | 'download'
  | 'preview'
  | 'archivePreview'
  | 'edit'
  | 'properties'
  | 'rename'
  | 'move'
  | 'copy'
  | 'archive'
  | 'extract'
  | 'checksum'
  | 'trash';

export type EntryAction = {
  name: EntryActionName;
  icon: SvgIconComponent;
  visible: (entry: FileEntry) => boolean;
  destructive?: boolean;
};

export const entryActions: EntryAction[] = [
  { name: 'download', icon: DownloadOutlined, visible: (entry) => entry.kind === 'file' },
  { name: 'archivePreview', icon: FolderZipOutlined, visible: (entry) => entry.isArchive },
  { name: 'preview', icon: VisibilityOutlined, visible: (entry) => entry.kind === 'file' && entry.previewable },
  { name: 'edit', icon: EditOutlined, visible: (entry) => entry.kind === 'file' && entry.editable },
  { name: 'properties', icon: InfoOutlined, visible: () => true },
  { name: 'rename', icon: DriveFileRenameOutline, visible: () => true },
  { name: 'move', icon: DriveFileMoveOutlined, visible: () => true },
  { name: 'copy', icon: ContentCopyOutlined, visible: () => true },
  { name: 'archive', icon: FolderZipOutlined, visible: (entry) => entry.kind === 'directory' },
  { name: 'extract', icon: UnarchiveOutlined, visible: (entry) => entry.isArchive },
  { name: 'checksum', icon: Fingerprint, visible: (entry) => entry.kind === 'file' },
  { name: 'trash', icon: DeleteOutline, visible: () => true }
];

export function entryMenuActions(entry: FileEntry, mutable: boolean, editorAvailable: boolean) {
  return entryActions.filter((item) => {
    if (!item.visible(entry)) return false;
    if (item.name === 'edit') return editorAvailable;
    if (item.name === 'properties' || item.name === 'download' || item.name === 'preview' || item.name === 'archivePreview') return true;
    return mutable;
  });
}

export const hoverActionNames: EntryActionName[] = ['download', 'properties'];
