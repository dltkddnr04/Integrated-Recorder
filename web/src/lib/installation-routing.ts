import type { InstallationStatus } from '@/types/api'

/** Returns the route a browser should enter after installation and auth are known. */
export function installationRouteRedirect(
  pathname: string,
  installation: InstallationStatus,
  authenticated: boolean,
  bootstrapCompatibility = false,
): '/' | '/setup' | '/login' | undefined {
  if (pathname === '/setup') return installation.state === 'ready' ? '/' : undefined
  if (pathname === '/login' && bootstrapCompatibility) return '/setup'
  if (installation.state !== 'ready') return '/setup'
  if (pathname === '/login') return authenticated ? '/' : undefined
  return authenticated ? undefined : '/login'
}
