export interface DirectoryFile { file: File; relativePath: string }
export interface DirectorySelection { files: DirectoryFile[]; directories: string[] }

function validate(path: string) {
  const parts = path.split('/');
  if (!path || parts.length > 64 || parts.some(part => !part || part === '.' || part === '..' || part.includes(String.fromCharCode(92)) || part.includes(String.fromCharCode(0)))) throw new Error('invalid_path');
}

export function filesWithDirectories(files: File[]): DirectorySelection {
  if (files.length > 10000) throw new Error('batch_limit_exceeded');
  const directories = new Set<string>();
  const entries = files.map(file => {
    const relativePath = file.webkitRelativePath || file.name;
    validate(relativePath);
    const parts = relativePath.split('/');
    for (let i = 1; i < parts.length; i++) directories.add(parts.slice(0, i).join('/'));
    return { file, relativePath };
  });
  if (directories.size + entries.length > 10000) throw new Error('batch_limit_exceeded');
  return { files: entries, directories: [...directories] };
}

export async function droppedDirectories(items: DataTransferItemList): Promise<DirectorySelection> {
  // Capture entries before the browser clears its drop-event data store.
  const roots = Array.from(items).filter(item => item.kind === 'file').map(item => ({ entry: item.webkitGetAsEntry?.(), file: item.getAsFile() }));
  const result: DirectorySelection = { files: [], directories: [] };
  let visited = 0;
  const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    validate(relativePath);
    if (++visited > 10000) throw new Error('batch_limit_exceeded');
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) => (entry as FileSystemFileEntry).file(resolve, reject));
      result.files.push({ file, relativePath });
    } else if (entry.isDirectory) {
      result.directories.push(relativePath);
      const reader = (entry as FileSystemDirectoryEntry).createReader();
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
        if (!batch.length) break;
        for (const child of batch) await walk(child, relativePath);
      }
    }
  };
  for (const root of roots) {
    if (root.entry) await walk(root.entry, '');
    else if (root.file) { if (++visited > 10000) throw new Error('batch_limit_exceeded'); validate(root.file.name); result.files.push({ file: root.file, relativePath: root.file.name }); }
  }
  return result;
}

interface DirectoryHandle {
  kind: 'directory';
  name: string;
  values(): AsyncIterable<DirectoryHandle | { kind: 'file'; name: string; getFile(): Promise<File> }>;
}

export function supportsDirectoryPicker() {
  return typeof (window as unknown as { showDirectoryPicker?: unknown }).showDirectoryPicker === 'function';
}

export async function pickDirectory(signal: AbortSignal): Promise<DirectorySelection> {
  const picker = (window as unknown as { showDirectoryPicker(options: { mode: 'read' }): Promise<DirectoryHandle> }).showDirectoryPicker;
  const root = await picker.call(window, { mode: 'read' });
  const result: DirectorySelection = { files: [], directories: [] };
  const deadline = Date.now() + 30000;
  let visited = 0;
  const check = () => {
    signal.throwIfAborted();
    if (Date.now() > deadline) throw new Error('batch_limit_exceeded');
  };
  const walk = async (entry: DirectoryHandle | { kind: 'file'; name: string; getFile(): Promise<File> }, prefix: string): Promise<void> => {
    check();
    const relativePath = prefix ? `${prefix}/${entry.name}` : entry.name;
    validate(relativePath);
    if (++visited > 10000) throw new Error('batch_limit_exceeded');
    if (entry.kind === 'file') {
      const file = await entry.getFile();
      check();
      result.files.push({ file, relativePath });
    } else {
      result.directories.push(relativePath);
      for await (const child of entry.values()) await walk(child, relativePath);
    }
  };
  await walk(root, '');
  return result;
}
