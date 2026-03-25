import { useState, FormEvent } from 'react';
import { query, getDocuments, QueryResult, Document } from '../api';
import { ResultSidebar } from '../components/ResultSidebar';
import { ResultDetail } from '../components/ResultDetail';

export function QueryPage() {
  const [q, setQ] = useState('');
  const [results, setResults] = useState<QueryResult[]>([]);
  const [docMap, setDocMap] = useState<Record<string, Document>>({});
  const [selected, setSelected] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [searched, setSearched] = useState(false);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!q.trim()) return;
    setLoading(true);
    setError('');
    setSearched(true);
    try {
      const resp = await query(q.trim());
      const items = resp.results || [];
      setResults(items);
      setSelected(0);

      // Fetch document metadata
      if (items.length > 0) {
        const uniqueIds = [...new Set(items.map(r => r.documentId))];
        const docsResp = await getDocuments(uniqueIds);
        const map: Record<string, Document> = {};
        for (const doc of (docsResp.documents || [])) {
          map[doc.id] = doc;
        }
        setDocMap(map);
      } else {
        setDocMap({});
      }
    } catch (e) {
      setError(String(e));
      setResults([]);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div>
      <form onSubmit={handleSubmit} style={{ marginBottom: '1rem' }}>
        <fieldset role="group">
          <input
            type="text"
            placeholder="Search your knowledge base..."
            value={q}
            onChange={e => setQ(e.target.value)}
            required
          />
          <button type="submit" disabled={loading} aria-busy={loading}>
            {loading ? 'Searching...' : 'Search'}
          </button>
        </fieldset>
      </form>

      {error && <p style={{ color: 'red' }}>{error}</p>}

      {searched && results.length === 0 && !loading && !error && (
        <p>No results found.</p>
      )}

      {results.length > 0 && (
        <div style={{ display: 'flex', height: 'calc(100vh - 200px)' }}>
          <ResultSidebar
            results={results}
            docMap={docMap}
            selected={selected}
            onSelect={setSelected}
          />
          <ResultDetail
            result={results[selected]}
            doc={docMap[results[selected].documentId]}
          />
        </div>
      )}
    </div>
  );
}
