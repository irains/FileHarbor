import { lazy, Suspense } from 'react';
import { Navigate, Outlet, createBrowserRouter } from 'react-router-dom';
import { getRuntime } from './runtime';
import { TaskProvider } from './tasks/TaskProvider';
import { SessionProvider } from './session/SessionProvider';

const LoginPage = lazy(() => import('./components/LoginPage').then((module) => ({ default: module.LoginPage })));
const Workspace = lazy(() => import('./components/Workspace').then((module) => ({ default: module.Workspace })));

function RouteFallback() {
  return null;
}

function LoginRoute() {
  return <SessionProvider loginPage><Suspense fallback={<RouteFallback />}><LoginPage /></Suspense></SessionProvider>;
}

function ProtectedShell() {
  return <SessionProvider loginPage={false}><TaskProvider><Suspense fallback={<RouteFallback />}><Outlet /></Suspense></TaskProvider></SessionProvider>;
}

function UnknownRoute() {
  return <Navigate replace to="/" />;
}

export function createAppRouter() {
  const { basePath } = getRuntime();
  return createBrowserRouter([
    { path: '/login', element: <LoginRoute /> },
    {
      element: <ProtectedShell />,
      children: [
        { path: '/', element: <Workspace />, children: [
          { index: true, element: null },
          { path: 'd/*', element: null },
          { path: 'edit/*', element: null }
        ] }
      ]
    },
    { path: '*', element: <UnknownRoute /> }
  ], { basename: basePath || undefined });
}
