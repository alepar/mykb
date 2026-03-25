import { useEffect, useState } from 'react';
import { listDocuments, getStatus, Document, StatusResponse } from '../api';

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
            <th>URL</th>
            <th>Status</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {docs.map(doc => (
            <tr key={doc.id}>
              <td>{doc.title || '—'}</td>
              <td style={{ maxWidth: '300px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                <a href={doc.url} target="_blank" rel="noopener">{doc.url}</a>
              </td>
              <td>{doc.status}</td>
              <td>{doc.createdAt ? new Date(Number(doc.createdAt) * 1000).toLocaleDateString() : '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
