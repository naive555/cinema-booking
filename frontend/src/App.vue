<template>
  <div>
    <nav v-if="auth.isLoggedIn" style="padding:10px 20px;background:#1a1a2e;color:#fff;display:flex;gap:20px;align-items:center">
      <strong style="color:#e94560">Cinema</strong>
      <router-link to="/" style="color:#adf">Showtimes</router-link>
      <router-link v-if="auth.isAdmin" to="/admin" style="color:#fda">Admin</router-link>
      <span style="margin-left:auto;font-size:0.85em;color:#aaa">
        {{ auth.user?.email }}
        <span style="background:#444;padding:2px 6px;border-radius:3px;margin-left:4px">{{ auth.user?.role }}</span>
      </span>
      <button @click="logout" style="padding:4px 12px;background:#e94560;color:#fff;border:none;border-radius:4px">
        Logout
      </button>
    </nav>
    <router-view />
  </div>
</template>

<script setup>
import { useRouter } from 'vue-router'
import { useAuthStore } from './stores/auth'

const auth = useAuthStore()
const router = useRouter()

function logout() {
  auth.logout()
  router.push('/login')
}
</script>
