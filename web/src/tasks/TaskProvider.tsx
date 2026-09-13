import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiError, type JobRequest, type FileJob } from '../api/client';
import { useSession } from '../session/SessionProvider';
import { TaskPanel } from './TaskPanel';

const TaskContext = createContext<{ open: () => void; activeCount: number; failed: boolean; loading: boolean; submit: (request: Omit<JobRequest, 'key'>) => Promise<void> } | null>(null);
export function TaskProvider({ children }: { children: ReactNode }) {
  const client = useQueryClient();
  const { session } = useSession();
  const pending = useRef(new Map<string, string>());
  useEffect(() => () => { client.removeQueries({ queryKey: ['jobs'] }); }, [client]);
  const [open, setOpen] = useState(false);
  const query = useQuery({ queryKey: ['jobs'], queryFn: ({ signal }) => api.getJobs(signal), refetchInterval: query => query.state.data?.jobs.some(job => ['queued', 'running'].includes(job.state)) ? 1000 : open ? 5000 : false });
  const seen = useRef(new Map<string, string>());
  useEffect(() => {
    for (const job of query.data?.jobs || []) {
      const signature = `${job.state}:${job.published.length}`;
      if (seen.current.get(job.id) !== signature && (job.published.length || !['queued', 'running'].includes(job.state))) {
        void client.invalidateQueries({ queryKey: ['listing'] });
        void client.invalidateQueries({ queryKey: ['properties'] });
        void client.invalidateQueries({ queryKey: ['size-scan'] });
      }
      seen.current.set(job.id, signature);
    }
  }, [query.data, client]);
  const submit = async (request: Omit<JobRequest, 'key'>) => {
    const signature = JSON.stringify(request);
    const key = pending.current.get(signature) || crypto.randomUUID().replaceAll('-', '');
    pending.current.set(signature, key);
    try { await api.createJob({ ...request, key }); }
    catch (failure) {
      if (failure instanceof ApiError && failure.code === 'job_hidden') {
        pending.current.delete(signature);
        await client.invalidateQueries({ queryKey: ['jobs'] });
      }
      throw failure;
    }
    pending.current.delete(signature);
    setOpen(true); await client.invalidateQueries({ queryKey: ['jobs'] });
  };
  const retry = async (job: FileJob) => {
    const parent = job.sources[0].path.split('/').slice(0, -1).join('/');
    const listing = await api.getListing(parent);
    const entries = job.sources.map(source => {
      const entry = listing.entries.find(item => item.path === source.path);
      if (!entry) throw new Error('source unavailable');
      return { name: entry.name, version: entry.version };
    });
    await submit({ kind: job.kind, previous: job.id, listing_token: listing.listingToken, entries, destination: job.destination, name: job.name, target: job.target });
  };
  const remove = async (job: FileJob) => {
    await api.removeJob(job.id);
    await client.cancelQueries({ queryKey: ['jobs'] });
    client.setQueryData<{ ok: true; jobs: FileJob[] }>(['jobs'], previous => previous ? { ...previous, jobs: previous.jobs.filter(entry => entry.id !== job.id) } : previous);
    await client.invalidateQueries({ queryKey: ['jobs'] });
  };
  const activeCount = query.data?.jobs.filter(job => ['queued', 'running'].includes(job.state)).length || 0;
  return <TaskContext.Provider value={{ open: () => setOpen(true), submit, activeCount, failed: query.isError, loading: query.isPending }}>
    {children}
    <TaskPanel canMutate={session?.capabilities.mutate ?? false} open={open} onClose={() => setOpen(false)} jobs={query.data?.jobs || []} loading={query.isPending} failed={query.isError} refresh={() => query.refetch()} retry={retry} remove={remove} />
  </TaskContext.Provider>;
}
export function useTasks() { return useContext(TaskContext); }
