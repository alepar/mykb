import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { Nav } from './components/Nav';
import { StatusPage } from './pages/StatusPage';
import { IngestPage } from './pages/IngestPage';
import { QueryPage } from './pages/QueryPage';

export function App() {
  return (
    <BrowserRouter>
      <Nav />
      <main className="container" style={{ paddingLeft: 'calc(var(--pico-spacing) / 2)', paddingRight: 'calc(var(--pico-spacing) / 2)' }}>
        <Routes>
          <Route path="/" element={<StatusPage />} />
          <Route path="/ingest" element={<IngestPage />} />
          <Route path="/query" element={<QueryPage />} />
        </Routes>
      </main>
    </BrowserRouter>
  );
}
