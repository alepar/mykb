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
      <h2>Ingest URL</h2>
      <form onSubmit={handleSubmit}>
        <fieldset role="group">
          <input
            type="url"
            placeholder="https://example.com/article"
            value={url}
            onChange={e => setUrl(e.target.value)}
            required
          />
          <button type="submit" disabled={loading} aria-busy={loading}>
            {loading ? 'Submitting...' : 'Ingest'}
          </button>
        </fieldset>
      </form>
      {result && <p style={{ color: 'green' }}>{result}</p>}
      {error && <p style={{ color: 'red' }}>{error}</p>}
    </>
  );
}
