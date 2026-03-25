import { useEffect, useState } from 'react';
import { listDocuments, Document } from '../api';

export function StatusPage() {
  const [docs, setDocs] = useState<Document[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState('');

  const fetchDocs = async () => {
    try {
      const resp = await listDocuments(20);
      setDocs(resp.documents || []);
      setTotal(resp.total || 0);
      setError('');
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    fetchDocs();
    const interval = setInterval(fetchDocs, 10000);
    return () => clearInterval(interval);
  }, []);

  // Count statuses from recent docs (approximate)
  const statusCounts: Record<string, number> = {};
  for (const doc of docs) {
    statusCounts[doc.status] = (statusCounts[doc.status] || 0) + 1;
  }

  return (
    <>
      <h2>Status</h2>
      {error && <p style={{ color: 'red' }}>{error}</p>}
      <p>Total documents: <strong>{total}</strong></p>

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
