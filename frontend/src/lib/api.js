import axios from 'axios'
import { useAuthStore } from '../stores/auth'

const api = axios.create({ baseURL: '/api' })

// Attach JWT to every request
api.interceptors.request.use((config) => {
  const { token } = useAuthStore()
  if (token) config.headers.Authorization = `Bearer ${token}`
  return config
})

// On 401 → clear session and redirect to login
api.interceptors.response.use(null, (err) => {
  if (err.response?.status === 401) {
    useAuthStore().logout()
    window.location.href = '/login'
  }
  return Promise.reject(err)
})

export default api
