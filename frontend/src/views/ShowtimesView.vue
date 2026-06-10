<template>
  <div style="max-width:720px;margin:32px auto;padding:0 16px">
    <h2 style="color:#1a1a2e">Now Showing</h2>
    <p v-if="loading" style="color:#888">Loading showtimes…</p>
    <p v-if="error" style="color:#e94560">{{ error }}</p>
    <div
      v-for="st in showtimes" :key="st.id"
      style="background:#fff;border-radius:8px;padding:16px 20px;margin-bottom:12px;box-shadow:0 1px 4px rgba(0,0,0,.08);display:flex;justify-content:space-between;align-items:center"
    >
      <div>
        <div style="font-size:1.1em;font-weight:600;color:#1a1a2e">{{ st.movieTitle }}</div>
        <div style="color:#666;font-size:0.88em;margin-top:4px">{{ st.hall }} &nbsp;·&nbsp; {{ fmt(st.startTime) }}</div>
      </div>
      <router-link :to="`/showtimes/${st.id}/seats`" style="text-decoration:none">
        <button style="padding:8px 18px;background:#e94560;color:#fff;border:none;border-radius:4px;font-size:0.9rem">
          Book Seats
        </button>
      </router-link>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import api from '../lib/api'

const showtimes = ref([])
const loading   = ref(true)
const error     = ref('')

onMounted(async () => {
  try {
    const { data } = await api.get('/showtimes')
    showtimes.value = data.showtimes ?? []
  } catch {
    error.value = 'Failed to load showtimes'
  } finally {
    loading.value = false
  }
})

function fmt(iso) {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}
</script>
