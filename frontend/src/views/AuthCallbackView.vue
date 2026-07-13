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
  // The backend sends the JWT in the URL fragment (#token=...), not a query
  // param, so it's never sent to any server and never lands in access logs
  // or the Referer header on the next navigation.
  const token = new URLSearchParams(route.hash.slice(1)).get('token')
  if (token) {
    auth.setToken(token)
    router.replace('/')
  } else {
    router.replace('/login')
  }
})
</script>
