<template>
  <div style="max-width:380px;margin:80px auto;padding:32px;background:#fff;border-radius:8px;box-shadow:0 2px 12px rgba(0,0,0,.1)">
    <h2 style="margin-top:0;color:#1a1a2e">Cinema Booking</h2>

    <a href="/auth/google/login" style="display:block;text-decoration:none">
      <button style="width:100%;padding:12px;font-size:1rem;background:#4285f4;color:#fff;border:none;border-radius:4px">
        Sign in with Google
      </button>
    </a>

    <!-- Dev login — only compiled-in when VITE_DEV_AUTH=true -->
    <template v-if="devAuth">
      <hr style="margin:24px 0;border:none;border-top:1px solid #eee"/>
      <p style="color:#888;font-size:0.8em;margin:0 0 12px">Dev login (DEV_AUTH only)</p>
      <input
        v-model="email"
        type="email"
        placeholder="Email address"
        style="width:100%;padding:8px;margin-bottom:8px;border:1px solid #ccc;border-radius:4px"
      />
      <select v-model="role" style="width:100%;padding:8px;margin-bottom:12px;border:1px solid #ccc;border-radius:4px">
        <option value="USER">USER</option>
        <option value="ADMIN">ADMIN</option>
      </select>
      <button
        @click="devLogin"
        :disabled="loading"
        style="width:100%;padding:10px;background:#333;color:#fff;border:none;border-radius:4px;font-size:0.95rem"
      >{{ loading ? 'Signing in…' : 'Admin Login' }}</button>
      <p v-if="error" style="color:#e94560;margin-top:8px;font-size:0.9em">{{ error }}</p>
    </template>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import axios from 'axios'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const devAuth  = import.meta.env.VITE_DEV_AUTH === 'true'
const email    = ref('')
const role     = ref('USER')
const loading  = ref(false)
const error    = ref('')
const router   = useRouter()
const auth     = useAuthStore()

async function devLogin() {
  if (!email.value) { error.value = 'Email required'; return }
  loading.value = true
  error.value   = ''
  try {
    const { data } = await axios.post('/dev/token', { email: email.value, role: role.value })
    auth.setToken(data.token)
    router.push('/')
  } catch (e) {
    error.value = e.response?.data?.error || 'Login failed'
  } finally {
    loading.value = false
  }
}
</script>
