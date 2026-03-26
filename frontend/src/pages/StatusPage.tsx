import { useEffect, useState } from 'react';
import { listDocuments, getStatus, Document, StatusResponse } from '../api';

function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + '...' : s;
}

function relativeTime(unixStr: string): string {
  if (!unixStr) return '—';
  const seconds = Math.floor(Date.now() / 1000 - Number(unixStr));
  if (seconds < 60) return 'just now';
  if (seconds < 3600) return `${Math.floor(seconds / 60)}min ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}hr ago`;
  if (seconds < 172800) return 'yesterday';
  return `${Math.floor(seconds / 86400)}d ago`;
}

export function StatusPage() {
  const [docs, setDocs] = useState<Document[]>([]);
  const [total, setTotal] = useState(0);
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [error, setError] = useState('');

  const fetchData = async () => {
    try {
      const [docsResp, statusResp] = await Promise.all([
        listDocuments(20),
        getStatus(),
      ]);
      setDocs(docsResp.documents || []);
      setTotal(docsResp.total || 0);
      setStatus(statusResp);
      setError('');
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    fetchData();
    const interval = setInterval(fetchData, 10000);
    return () => clearInterval(interval);
  }, []);

  const counts = status?.document_counts || {};
  const statuses = ['DONE', 'PENDING', 'CRAWLING', 'EMBEDDING', 'INDEXING', 'ERROR'];

  return (
    <>
      <h2 className="text-xl font-semibold mb-3">Status</h2>
      {error && <p className="text-red-500">{error}</p>}

      <div className="flex flex-wrap gap-4 mb-4 text-sm">
        <div><span className="font-semibold">Documents:</span> {total}</div>
        <div><span className="font-semibold">Chunks:</span> {status?.total_chunks ?? '—'}</div>
        {statuses.map(s => {
          const count = counts[s];
          if (!count) return null;
          return <div key={s}><span className="font-semibold">{s}:</span> {count}</div>;
        })}
      </div>

      <h3 className="text-lg font-semibold mb-2">Recent Documents</h3>
      <table className="w-full text-sm text-left">
        <thead className="border-b border-gray-300 dark:border-gray-700">
          <tr>
            <th className="py-2 pr-3 font-medium">Title</th>
            <th className="py-2 pr-3 font-medium">Step</th>
            <th className="py-2 pr-3 font-medium">State</th>
            <th className="py-2 pr-3 font-medium">Error</th>
            <th className="py-2 font-medium">Updated</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-gray-200 dark:divide-gray-800">
          {docs.map(doc => (
            <tr key={doc.id}>
              <td className="py-1.5 pr-3" title={doc.title || undefined}>
                <a href={doc.url} target="_blank" rel="noopener">
                  {doc.title ? truncate(doc.title, 80) : '—'}
                </a>
              </td>
              <td className="py-1.5 pr-3">{doc.step}</td>
              <td className="py-1.5 pr-3">{doc.state}</td>
              <td className="py-1.5 pr-3 text-red-500 dark:text-red-400" title={doc.error || undefined}>
                {doc.error ? truncate(doc.error, 30) : ''}
              </td>
              <td className="py-1.5 text-gray-500 dark:text-gray-400">{relativeTime(doc.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
