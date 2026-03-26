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
      <h2>Status</h2>
      {error && <p style={{ color: 'red' }}>{error}</p>}

      <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
        <div><strong>Documents:</strong> {total}</div>
        <div><strong>Chunks:</strong> {status?.total_chunks ?? '—'}</div>
        {statuses.map(s => {
          const count = counts[s];
          if (!count) return null;
          return <div key={s}><strong>{s}:</strong> {count}</div>;
        })}
      </div>

      <h3>Recent Documents</h3>
      <table>
        <thead>
          <tr>
            <th>Title</th>
            <th>Step</th>
            <th>State</th>
            <th>Error</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {docs.map(doc => (
            <tr key={doc.id}>
              <td title={doc.title || undefined}>
                <a href={doc.url} target="_blank" rel="noopener">
                  {doc.title ? truncate(doc.title, 50) : '—'}
                </a>
              </td>
              <td>{doc.step}</td>
              <td>{doc.state}</td>
              <td title={doc.error || undefined}>{doc.error ? truncate(doc.error, 30) : ''}</td>
              <td>{relativeTime(doc.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
