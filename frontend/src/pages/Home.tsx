import { Navigate } from 'react-router-dom';

export default function HomePage() {
  // replace (not push) so Back doesn't bounce off "/" → "/services" again.
  return <Navigate to="/services" replace />;
}
