import { useState, FormEvent } from 'react';
import { ingestURL } from '../api';

export function IngestPage() {
  const [url, setUrl] = useState('');
  const [result, setResult] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!url.trim()) return;
    setLoading(true);
    setResult('');
    setError('');
    try {
      const resp = await ingestURL(url.trim());
      setResult(`Submitted! Document ID: ${resp.id}`);
      setUrl('');
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <>
      <h2 className="text-xl font-semibold mb-3">Ingest URL</h2>
      <form onSubmit={handleSubmit} className="flex gap-2 max-w-2xl">
        <input
          type="url"
          placeholder="https://example.com/article"
          value={url}
          onChange={e => setUrl(e.target.value)}
          required
          className="flex-1 rounded border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-900 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        <button
          type="submit"
          disabled={loading}
          className="rounded bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {loading ? 'Submitting...' : 'Ingest'}
        </button>
      </form>
      {result && <p className="text-green-600 dark:text-green-400 mt-2">{result}</p>}
      {error && <p className="text-red-500 mt-2">{error}</p>}
    </>
  );
}
