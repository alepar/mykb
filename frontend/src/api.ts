import { API_BASE } from './config';

interface ListDocumentsResponse {
  documents: Document[];
  total: number;
}

interface QueryResponse {
  results: QueryResult[];
}

interface GetDocumentsResponse {
  documents: Document[];
}

export interface Document {
  id: string;
  url: string;
  title: string;
  status: string;
  error: string;
  chunkCount: number;
  createdAt: string; // unix timestamp as string
  crawledAt: string;
  updatedAt: string;
}

export interface QueryResult {
  chunkId: string;
  documentId: string;
  chunkIndex: number;
  chunkIndexEnd: number;
  score: number;
  text: string;
}

async function post<T>(path: string, body: object): Promise<T> {
  const resp = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`${resp.status}: ${text}`);
  }
  return resp.json();
}

export async function listDocuments(limit: number, offset = 0): Promise<ListDocumentsResponse> {
  return post('/mykb.v1.KBService/ListDocuments', { limit, offset });
}

export async function query(q: string): Promise<QueryResponse> {
  return post('/mykb.v1.KBService/Query', { query: q, topK: 10 });
}

export async function getDocuments(ids: string[]): Promise<GetDocumentsResponse> {
  return post('/mykb.v1.KBService/GetDocuments', { ids });
}

export async function ingestURL(url: string): Promise<{ id: string }> {
  const resp = await fetch(`${API_BASE}/api/ingest`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url }),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`${resp.status}: ${text}`);
  }
  return resp.json();
}
