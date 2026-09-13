import { useIsMutating, useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api, ApiError, type Favorite } from '../api/client';

const favoritesKey = ['favorites'];
const mutationKey = ['favorites', 'change'];
// Guard clicks in different consumers before React renders their pending state.
const changing = new WeakSet<QueryClient>();
type Change = { kind: 'add'; path: string; label: string }
  | { kind: 'rename'; entry: Favorite; label: string }
  | { kind: 'remove'; entry: Favorite };

export function useFavorites() {
  const client = useQueryClient();
  const query = useQuery({ queryKey: favoritesKey, queryFn: api.getFavorites, staleTime: 0 });
  const pending = useIsMutating({ mutationKey }) > 0;
  const mutation = useMutation({
    mutationKey,
    mutationFn: async (change: Change) => {
      await client.cancelQueries({ queryKey: favoritesKey });
      if (change.kind === 'add') return api.addFavorite(change.path, change.label);
      if (change.kind === 'rename') return api.renameFavorite(change.entry, change.label);
      return api.removeFavorite(change.entry);
    },
    onSuccess: async ({ entry }, change) => {
      await client.cancelQueries({ queryKey: favoritesKey });
      client.setQueryData<Awaited<ReturnType<typeof api.getFavorites>>>(favoritesKey, (previous) => {
        if (!previous) return previous;
        const entries = previous.entries.filter(item => item.id !== entry.id);
        if (change.kind !== 'remove') {
          const index = previous.entries.findIndex(item => item.id === entry.id);
          entries.splice(index < 0 ? entries.length : index, 0, entry);
        }
        return { ...previous, entries };
      });
    },
    onSettled: () => client.invalidateQueries({ queryKey: favoritesKey }),
  });
  const change = async (value: Change) => {
    if (changing.has(client)) return false;
    changing.add(client);
    try { await mutation.mutateAsync(value); return true; }
    catch { return false; }
    finally { changing.delete(client); }
  };
  return {
    query, pending, change,
    error: mutation.error ? (mutation.error instanceof ApiError ? mutation.error.code : 'generic') : null,
    errorPath: mutation.variables && (mutation.variables.kind === 'add' ? mutation.variables.path : mutation.variables.entry.path),
  };
}
