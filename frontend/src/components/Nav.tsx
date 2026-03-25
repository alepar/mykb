import { Link } from 'react-router-dom';

export function Nav() {
  return (
    <nav className="container">
      <ul>
        <li><strong>MyKB</strong></li>
      </ul>
      <ul>
        <li><Link to="/">Status</Link></li>
        <li><Link to="/ingest">Ingest</Link></li>
        <li><Link to="/query">Query</Link></li>
      </ul>
    </nav>
  );
}
