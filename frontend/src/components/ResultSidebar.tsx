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
      <div style={{ borderBottom: '1px solid var(--pico-muted-border-color)', marginBottom: '0.5rem', paddingBottom: '0.5rem' }}>
        {results.map((r, i) => {
          const doc = docMap[r.documentId];
          const title = doc?.title || r.documentId;
          const isActive = i === selected;
          return (
            <div
              key={`${r.documentId}-${r.chunkIndex}`}
              onClick={() => onSelect(i)}
              style={{
                padding: '0.35rem 0.5rem',
                cursor: 'pointer',
                borderRadius: '4px',
                backgroundColor: isActive ? 'var(--pico-primary-background)' : 'transparent',
                color: isActive ? 'var(--pico-primary-inverse)' : 'inherit',
                fontSize: '0.85rem',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              <span style={{ color: isActive ? 'inherit' : 'var(--pico-del-color)' }}>#{i + 1}</span>{' '}
              <span style={{ opacity: 0.7 }}>{r.score.toFixed(2)}</span>{' '}
              {title}
            </div>
          );
        })}
      </div>
    );
  }

  return (
    <aside style={{ width: '300px', minWidth: '300px', borderRight: '1px solid var(--pico-muted-border-color)', overflowY: 'auto', padding: '0.5rem' }}>
      {results.map((r, i) => {
        const doc = docMap[r.documentId];
        const title = doc?.title || r.documentId;
        const host = doc ? domain(doc.url) : '';
        const isActive = i === selected;
        return (
          <div
            key={`${r.documentId}-${r.chunkIndex}`}
            onClick={() => onSelect(i)}
            style={{
              padding: '0.5rem',
              cursor: 'pointer',
              borderRadius: '4px',
              backgroundColor: isActive ? 'var(--pico-primary-background)' : 'transparent',
              color: isActive ? 'var(--pico-primary-inverse)' : 'inherit',
              marginBottom: '0.25rem',
            }}
          >
            <div style={{ fontSize: '0.8rem' }}>
              <span style={{ color: isActive ? 'inherit' : 'var(--pico-del-color)' }}>#{i + 1}</span>{' '}
              <span style={{ opacity: 0.7 }}>{`{${r.score.toFixed(2)}}`}</span>{' '}
              <span>{host}</span>
            </div>
            <div style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {title}
            </div>
          </div>
        );
      })}
    </aside>
  );
}
