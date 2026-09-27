import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LoginPage } from './login'
import { ToastProvider } from '@/components/ui/toast'

const mocks = vi.hoisted(() => ({
  session: vi.fn(), bootstrap: vi.fn(), login: vi.fn(), logout: vi.fn(), navigate: vi.fn(), search: {} as { mode?: 'bootstrap' },
}))
vi.mock('@/api', async importOriginal => {
  const actual = await importOriginal<typeof import('@/api')>()
  return { ...actual, authAPI: { ...actual.authAPI, session: mocks.session, bootstrap: mocks.bootstrap, login: mocks.login, logout: mocks.logout } }
})
vi.mock('@tanstack/react-router', async importOriginal => ({
  ...await importOriginal<typeof import('@tanstack/react-router')>(),
  useNavigate: () => mocks.navigate,
  useSearch: () => mocks.search,
}))

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ToastProvider><LoginPage /></ToastProvider></QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.search = {}
  mocks.session.mockResolvedValue({ auth_enabled: true, authenticated: false, needs_bootstrap: false })
  mocks.bootstrap.mockResolvedValue({ auth_enabled: true, authenticated: true, needs_bootstrap: false, csrf_token: 'boot-csrf' })
  mocks.login.mockResolvedValue({ auth_enabled: true, authenticated: true, needs_bootstrap: false, csrf_token: 'login-csrf' })
})

describe('authentication screens', () => {
  it('completes the first-run bootstrap form without trying to read the token path', async () => {
    mocks.search = { mode: 'bootstrap' }
    mocks.session.mockResolvedValue({ auth_enabled: true, authenticated: false, needs_bootstrap: true, bootstrap_token_path: '/data/bootstrap-token' })
    renderLogin()
    fireEvent.change(await screen.findByLabelText('Bootstrap token'), { target: { value: 'one-time-token' } })
    fireEvent.change(screen.getByLabelText('관리자 비밀번호'), { target: { value: 'a-strong-passphrase' } })
    fireEvent.change(screen.getByLabelText('비밀번호 확인'), { target: { value: 'a-strong-passphrase' } })
    fireEvent.click(screen.getByRole('button', { name: '서버 초기화' }))
    await waitFor(() => expect(mocks.bootstrap).toHaveBeenCalledWith('one-time-token', 'a-strong-passphrase'))
    expect(mocks.navigate).toHaveBeenCalledWith({ to: '/' })
    expect(screen.getByText('/data/bootstrap-token')).toBeInTheDocument()
  })

  it('submits the login password and navigates into the application', async () => {
    renderLogin()
    fireEvent.change(await screen.findByLabelText('관리자 비밀번호'), { target: { value: 'a-strong-passphrase' } })
    fireEvent.click(screen.getByRole('button', { name: '로그인' }))
    await waitFor(() => expect(mocks.login).toHaveBeenCalledWith('a-strong-passphrase'))
    expect(mocks.bootstrap).not.toHaveBeenCalled()
    expect(mocks.navigate).toHaveBeenCalledWith({ to: '/' })
  })
})
