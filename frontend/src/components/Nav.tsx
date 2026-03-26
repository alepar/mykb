import { Link } from 'react-router-dom';

export function Nav() {
  return (
    <nav className="bg-gray-900 text-gray-100 dark:bg-gray-900">
      <div className="max-w-7xl mx-auto px-4 flex items-center justify-between h-12">
        <span className="font-bold text-lg">MyKB</span>
        <div className="flex gap-4 text-sm">
          <Link to="/" className="text-gray-300 hover:text-white">Status</Link>
          <Link to="/ingest" className="text-gray-300 hover:text-white">Ingest</Link>
          <Link to="/query" className="text-gray-300 hover:text-white">Query</Link>
        </div>
      </div>
    </nav>
  );
}
