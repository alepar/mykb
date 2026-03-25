// In production, the API is at a different domain.
// In dev, Vite proxies /mykb.v1.KBService and /api to localhost:9091.
export const API_BASE = import.meta.env.PROD
  ? 'http://api.mykb.k3s'
  : '';
