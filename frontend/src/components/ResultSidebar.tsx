import { QueryResult, Document } from '../api';

interface Props {
  results: QueryResult[];
  docMap: Record<string, Document>;
  selected: number;
  onSelect: (index: number) => void;
  compact?: boolean;
}

function domain(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

export function ResultSidebar({ results, docMap, selected, onSelect, compact }: Props) {
  if (compact) {
    return (
      <div className="border-b border-gray-200 dark:border-gray-700 mb-2 pb-2">
        {results.map((r, i) => {
          const doc = docMap[r.documentId];
          const title = doc?.title || r.documentId;
          const isActive = i === selected;
          return (
            <div
              key={`${r.documentId}-${r.chunkIndex}`}
              onClick={() => onSelect(i)}
              className={`px-2 py-1 cursor-pointer rounded text-sm truncate ${
                isActive
                  ? 'bg-blue-600 text-white'
                  : 'hover:bg-gray-100 dark:hover:bg-gray-800'
              }`}
            >
              <span className={isActive ? '' : 'text-gray-400'}>#{i + 1}</span>{' '}
              <span className="opacity-70">{r.score.toFixed(2)}</span>{' '}
              {title}
            </div>
          );
        })}
      </div>
    );
  }

  return (
    <aside className="w-72 min-w-72 border-r border-gray-200 dark:border-gray-700 overflow-y-auto p-2">
      {results.map((r, i) => {
        const doc = docMap[r.documentId];
        const title = doc?.title || r.documentId;
        const host = doc ? domain(doc.url) : '';
        const isActive = i === selected;
        return (
          <div
            key={`${r.documentId}-${r.chunkIndex}`}
            onClick={() => onSelect(i)}
            className={`p-2 cursor-pointer rounded mb-1 ${
              isActive
                ? 'bg-blue-600 text-white'
                : 'hover:bg-gray-100 dark:hover:bg-gray-800'
            }`}
          >
            <div className="text-xs">
              <span className={isActive ? '' : 'text-gray-400'}>#{i + 1}</span>{' '}
              <span className="opacity-70">{`{${r.score.toFixed(2)}}`}</span>{' '}
              <span>{host}</span>
            </div>
            <div className="text-sm truncate">
              {title}
            </div>
          </div>
        );
      })}
    </aside>
  );
}
