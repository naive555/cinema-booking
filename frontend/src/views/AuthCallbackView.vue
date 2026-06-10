<template>
  <p style="padding:48px;text-align:center;color:#555">Signing you in…</p>
</template>

<script setup>
import { onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const route  = useRoute()
const router = useRouter()
const auth   = useAuthStore()

onMounted(() => {
  const token = route.query.token
  if (token) {
    auth.setToken(token)
    router.replace('/')
  } else {
    router.replace('/login')
  }
})
</script>
