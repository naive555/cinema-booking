import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem('token') || null)
  const user = ref(null)

  // Hydrate user from stored token on page load
  if (token.value) _decodeToken(token.value)

  const isLoggedIn = computed(() => !!token.value)
  const isAdmin    = computed(() => user.value?.role === 'ADMIN')

  function setToken(jwt) {
    token.value = jwt
    localStorage.setItem('token', jwt)
    _decodeToken(jwt)
  }

  function logout() {
    token.value = null
    user.value  = null
    localStorage.removeItem('token')
  }

  function _decodeToken(jwt) {
    try {
      const payload = JSON.parse(atob(jwt.split('.')[1]))
      user.value = { id: payload.sub, email: payload.email, role: payload.role }
    } catch {
      user.value = null
    }
  }

  return { token, user, isLoggedIn, isAdmin, setToken, logout }
})
