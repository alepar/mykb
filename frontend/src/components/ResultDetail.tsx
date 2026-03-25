import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { QueryResult, Document } from '../api';

interface Props {
  result: QueryResult;
  doc?: Document;
}

function chunkPosition(r: QueryResult, chunkCount: number): string {
  if (chunkCount <= 0) return '';
  if (r.chunkIndexEnd > r.chunkIndex + 1) {
    return `${r.chunkIndex + 1}-${r.chunkIndexEnd}/${chunkCount}`;
  }
  return `${r.chunkIndex + 1}/${chunkCount}`;
}

function formatDate(timestamp: string): string {
  if (!timestamp || timestamp === '0') return '';
  return new Date(Number(timestamp) * 1000).toLocaleDateString();
}

export function ResultDetail({ result, doc }: Props) {
  const title = doc?.title || result.documentId;
  const url = doc?.url || '';
  const pos = doc ? chunkPosition(result, doc.chunkCount) : '';
  const created = doc ? formatDate(doc.createdAt) : '';
  const updated = doc ? formatDate(doc.updatedAt) : '';

  return (
    <div style={{ flex: 1, padding: '1rem', overflowY: 'auto' }}>
      {/* Header */}
      <div style={{ marginBottom: '1rem', borderBottom: '1px solid var(--pico-muted-border-color)', paddingBottom: '0.5rem' }}>
        <h3 style={{ margin: 0 }}>
          {url ? <a href={url} target="_blank" rel="noopener">{title}</a> : title}
        </h3>
        {url && <div style={{ fontSize: '0.85rem', color: 'var(--pico-muted-color)' }}>{url}</div>}
        <div style={{ fontSize: '0.8rem', color: 'var(--pico-muted-color)' }}>
          {pos && <span>Chunks {pos}</span>}
          {created && <span style={{ marginLeft: '1rem' }}>Added {created}</span>}
          {updated && <span style={{ marginLeft: '1rem' }}>Ingested {updated}</span>}
        </div>
      </div>

      {/* Markdown body */}
      <article>
        <ReactMarkdown remarkPlugins={[remarkGfm]}>
          {result.text}
        </ReactMarkdown>
      </article>
    </div>
  );
}
