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
    <div className="flex-1 p-4 overflow-y-auto">
      {/* Header */}
      <div className="mb-4 border-b border-gray-200 dark:border-gray-700 pb-2">
        <h3 className="text-lg font-semibold m-0">
          {url ? <a href={url} target="_blank" rel="noopener">{title}</a> : title}
        </h3>
        {url && <div className="text-sm text-gray-500 dark:text-gray-400">{url}</div>}
        <div className="text-xs text-gray-500 dark:text-gray-400">
          {pos && <span>Chunks {pos}</span>}
          {created && <span className="ml-4">Added {created}</span>}
          {updated && <span className="ml-4">Ingested {updated}</span>}
        </div>
      </div>

      {/* Markdown body */}
      <article className="prose dark:prose-invert max-w-none">
        <ReactMarkdown remarkPlugins={[remarkGfm]}>
          {result.text}
        </ReactMarkdown>
      </article>
    </div>
  );
}
