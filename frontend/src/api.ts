export type User = { id: number; email: string; role: 'admin' | 'developer' | 'viewer' }
export type Service = { id: string; slug: string; name: string; status: string; url: string; coolify_application_uuid?: string; coolify_url?: string; created_at: string; cpu_percent?: number; memory_bytes?: number; pids?: number; resource_available?: boolean }
export type Deployment = { id: string; service_id: string; slug: string; sequence: number; status: string; error: string; created_at: string }
export type ManagedUser = { id: number; email: string; role: User['role']; disabled: boolean; created_at: string }
export type EnvironmentVariable = { name: string; configured: boolean }
export type CoolifySettings = { base_url: string; project_uuid: string; environment_uuid: string; server_uuid: string; registry_prefix: string; api_token_configured: boolean; registry_username_configured: boolean; registry_password_configured: boolean }

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const csrf = document.cookie.split('; ').find(value => value.startsWith('aihub_csrf='))?.split('=').slice(1).join('=')
  const mutating = ['POST', 'PUT', 'PATCH', 'DELETE'].includes((init.method ?? 'GET').toUpperCase())
  const response = await fetch(`/api/v1${path}`, { credentials: 'include', ...init, headers: { ...(init.body instanceof FormData ? {} : { 'Content-Type': 'application/json' }), ...(mutating && csrf ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {}), ...init.headers } })
  const body = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(body.message ?? `Request failed (${response.status})`)
  return body as T
}

export const api = {
  me: () => request<{ user: User }>('/auth/me'),
  login: (email: string, password: string) => request<{ user: User }>('/auth/login', { method: 'POST', body: JSON.stringify({ email, password }) }),
  logout: () => request('/auth/logout', { method: 'POST' }),
  overview: () => request<{ services: number; running: number; deployments: number; queued: number; host: { cpu_percent: number; memory_percent: number; disk_percent: number; memory_bytes?: number; disk_used_bytes?: number; disk_total_bytes?: number; docker_version?: string; docker_containers?: number; docker_images?: number; status: string } }>('/admin/overview'),
  services: () => request<{ items: Service[] }>('/services'),
  deployments: () => request<{ items: Deployment[] }>('/deployments'),
  tokens: () => request<{ items: { id: number; name: string; created_at: string; revoked: boolean }[] }>('/tokens'),
  users: () => request<{ items: ManagedUser[] }>('/users'),
  createUser: (email: string, password: string, role: User['role']) => request('/users', { method: 'POST', body: JSON.stringify({ email, password, role }) }),
  updateUser: (id: number, patch: { role?: User['role']; disabled?: boolean }) => request(`/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),
  serviceAction: (id: string, action: 'start' | 'stop' | 'restart' | 'delete') => request(`/services/${id}/actions`, { method: 'POST', body: JSON.stringify({ action }) }),
  serviceEnv: (id: string) => request<{ items: EnvironmentVariable[]; values_hidden: boolean }>(`/services/${id}/env`),
  patchServiceEnv: (id: string, set: Record<string,string>, remove: string[]) => request<{ status: string; redeploy_required: boolean }>(`/services/${id}/env`, { method: 'PATCH', body: JSON.stringify({ set, delete: remove }) }),
  createToken: (name: string) => request<{ id: number; name: string; token: string }>('/tokens', { method: 'POST', body: JSON.stringify({ name }) }),
  revokeToken: (id: number) => request(`/tokens/${id}`, { method: 'DELETE' }),
  coolifySettings: () => request<CoolifySettings>('/admin/integrations/coolify'),
  saveCoolifySettings: (settings: Record<string, string>) => request('/admin/integrations/coolify', { method: 'PUT', body: JSON.stringify(settings) }),
  testCoolifySettings: () => request<{ status: string }>('/admin/integrations/coolify/test', { method: 'POST', body: '{}' }),
}
