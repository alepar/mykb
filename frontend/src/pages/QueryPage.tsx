import { useState, useRef, useEffect, FormEvent } from 'react';
import { query, getDocuments, QueryResult, Document } from '../api';
import { ResultSidebar } from '../components/ResultSidebar';
import { ResultDetail } from '../components/ResultDetail';

function useIsMobile(breakpoint = 768) {
  const [isMobile, setIsMobile] = useState(window.innerWidth < breakpoint);
  useEffect(() => {
    const handler = () => setIsMobile(window.innerWidth < breakpoint);
    window.addEventListener('resize', handler);
    return () => window.removeEventListener('resize', handler);
  }, [breakpoint]);
  return isMobile;
}

export function QueryPage() {
  const [q, setQ] = useState('');
  const [results, setResults] = useState<QueryResult[]>([]);
  const [docMap, setDocMap] = useState<Record<string, Document>>({});
  const [selected, setSelected] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [searched, setSearched] = useState(false);
  const detailRef = useRef<HTMLDivElement>(null);
  const isMobile = useIsMobile();

  const handleSelect = (index: number) => {
    setSelected(index);
    if (isMobile && detailRef.current) {
      detailRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  };

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
      <form onSubmit={handleSubmit} className="flex gap-2 mb-4">
        <input
          type="text"
          placeholder="Search your knowledge base..."
          value={q}
          onChange={e => setQ(e.target.value)}
          required
          className="flex-1 rounded border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <button
          type="submit"
          disabled={loading}
          className="rounded bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {loading ? 'Searching...' : 'Search'}
        </button>
      </form>

      {error && <p className="text-red-500">{error}</p>}

      {searched && results.length === 0 && !loading && !error && (
        <p className="text-gray-500">No results found.</p>
      )}

      {results.length > 0 && (
        isMobile ? (
          <div>
            <ResultSidebar
              results={results}
              docMap={docMap}
              selected={selected}
              onSelect={handleSelect}
              compact
            />
            <div ref={detailRef}>
              <ResultDetail
                result={results[selected]}
                doc={docMap[results[selected].documentId]}
              />
            </div>
          </div>
        ) : (
          <div className="flex" style={{ height: 'calc(100vh - 200px)' }}>
            <ResultSidebar
              results={results}
              docMap={docMap}
              selected={selected}
              onSelect={handleSelect}
            />
            <ResultDetail
              result={results[selected]}
              doc={docMap[results[selected].documentId]}
            />
          </div>
        )
      )}
    </div>
  );
}
