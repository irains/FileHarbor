import { afterEach, describe, expect, it, vi } from 'vitest';
import { filesWithDirectories, droppedDirectories, pickDirectory } from './directoryEntries';

function file(name: string, relative: string) {
  const item = new File(['data'], name);
  Object.defineProperty(item, 'webkitRelativePath', { value: relative });
  return item;
}

afterEach(() => vi.unstubAllGlobals());

describe('directory upload enumeration', () => {
  it('preserves empty directories from native handles and honors cancellation', async () => {
    const root = { kind: 'directory', name: 'root', async *values() {
      yield { kind: 'directory', name: 'empty', async *values() {} };
    } };
    vi.stubGlobal('showDirectoryPicker', vi.fn().mockResolvedValue(root));
    expect(await pickDirectory(new AbortController().signal)).toEqual({ files: [], directories: ['root', 'root/empty'] });
    const controller = new AbortController();
    controller.abort();
    await expect(pickDirectory(controller.signal)).rejects.toThrow();
  });

  it('keeps equal basenames in different folders distinct', () => {
    const result = filesWithDirectories([file('data.txt', 'root/a/data.txt'), file('data.txt', 'root/b/data.txt')]);
    expect(result.directories).toEqual(['root', 'root/a', 'root/b']);
    expect(result.files.map(item => item.relativePath)).toEqual(['root/a/data.txt', 'root/b/data.txt']);
    expect(() => filesWithDirectories([file('data', '../data')])).toThrow();
  });
  it('reads every directory batch and retains empty directories', async () => {
    const empty = { name: 'empty', isDirectory: true, isFile: false, createReader: () => ({ readEntries: (resolve: (entries: unknown[]) => void) => resolve([]) }) };
    const child = { name: 'data.txt', isDirectory: false, isFile: true, file: (resolve: (value: File) => void) => resolve(file('data.txt', '')) };
    const root = { name: 'root', isDirectory: true, isFile: false, createReader: () => {
      const batches = [[empty], [child], []];
      return { readEntries: (resolve: (entries: unknown[]) => void) => resolve(batches.shift()!) };
    } };
    const items = [{ kind: 'file', webkitGetAsEntry: () => root, getAsFile: () => null }] as unknown as DataTransferItemList;
    const result = await droppedDirectories(items);
    expect(result.directories).toEqual(['root', 'root/empty']);
    expect(result.files[0].relativePath).toBe('root/data.txt');
  });
});
