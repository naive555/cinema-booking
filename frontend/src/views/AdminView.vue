<template>
  <div style="max-width:960px;margin:32px auto;padding:0 16px">
    <h2 style="color:#1a1a2e">Admin — Bookings</h2>

    <!-- Filters -->
    <div style="display:flex;gap:8px;margin-bottom:16px;flex-wrap:wrap;background:#fff;padding:12px;border-radius:8px;box-shadow:0 1px 4px rgba(0,0,0,.08)">
      <input v-model="f.movieTitle" placeholder="Movie title" style="padding:6px 10px;border:1px solid #ccc;border-radius:4px;flex:1;min-width:140px"/>
      <input v-model="f.date" type="date" style="padding:6px 10px;border:1px solid #ccc;border-radius:4px"/>
      <input v-model="f.userId" placeholder="User ID (hex)" style="padding:6px 10px;border:1px solid #ccc;border-radius:4px;flex:1;min-width:160px"/>
      <button @click="search" style="padding:6px 16px;background:#1a1a2e;color:#fff;border:none;border-radius:4px">Search</button>
      <button @click="clearF" style="padding:6px 14px;background:#ddd;border:none;border-radius:4px">Clear</button>
    </div>

    <p v-if="bLoading" style="color:#888">Loading…</p>
    <p v-if="bError"   style="color:#e94560">{{ bError }}</p>

    <div v-if="!bLoading" style="font-size:0.85em;color:#555;margin-bottom:8px">
      {{ total }} booking(s) &nbsp;·&nbsp; page {{ page }} / {{ totalPages }}
    </div>

    <table v-if="bookings.length" style="width:100%;border-collapse:collapse;background:#fff;border-radius:8px;overflow:hidden;box-shadow:0 1px 4px rgba(0,0,0,.08);font-size:0.88em">
      <thead style="background:#f5f5f5">
        <tr>
          <th v-for="h in bHeaders" :key="h" style="padding:10px 12px;text-align:left;font-weight:600;color:#555">{{ h }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="b in bookings" :key="b.id" style="border-top:1px solid #eee">
          <td style="padding:8px 12px;font-family:monospace;font-size:0.9em">{{ b.id.slice(-8) }}</td>
          <td style="padding:8px 12px">
            <span style="font-weight:500">{{ showtimeMap[b.showtimeId] ?? '—' }}</span>
            <span style="display:block;font-family:monospace;font-size:0.78em;color:#999">{{ b.showtimeId.slice(-8) }}</span>
          </td>
          <td style="padding:8px 12px;font-family:monospace">{{ b.seatId.slice(-8) }}</td>
          <td style="padding:8px 12px">
            <div style="display:flex;align-items:center;gap:6px">
              <span style="font-family:monospace;font-size:0.82em;color:#333">{{ b.userId }}</span>
              <button
                @click="copyText(b.userId)"
                :title="copied === b.userId ? 'Copied!' : 'Copy ID'"
                style="flex-shrink:0;padding:2px 6px;font-size:0.75em;border:1px solid #ccc;border-radius:3px;background:#f5f5f5;cursor:pointer;color:#555"
              >{{ copied === b.userId ? '✓' : 'copy' }}</button>
            </div>
          </td>
          <td style="padding:8px 12px;color:#666">{{ fmt(b.createdAt) }}</td>
        </tr>
      </tbody>
    </table>
    <p v-else-if="!bLoading" style="color:#888">No bookings match this filter.</p>

    <div style="display:flex;gap:8px;margin-top:12px">
      <button @click="prevPage" :disabled="page<=1" style="padding:6px 14px;border:1px solid #ccc;background:#fff;border-radius:4px">Prev</button>
      <button @click="nextPage" :disabled="page>=totalPages" style="padding:6px 14px;border:1px solid #ccc;background:#fff;border-radius:4px">Next</button>
    </div>

    <!-- Audit log section -->
    <h3 style="color:#1a1a2e;margin-top:36px">Audit Logs</h3>
    <div style="display:flex;gap:8px;margin-bottom:12px">
      <select v-model="auditAction" style="padding:6px 10px;border:1px solid #ccc;border-radius:4px">
        <option value="">All actions</option>
        <option v-for="a in auditActions" :key="a" :value="a">{{ a }}</option>
      </select>
      <button @click="loadAudit" style="padding:6px 16px;background:#1a1a2e;color:#fff;border:none;border-radius:4px">Filter</button>
    </div>

    <p v-if="aLoading" style="color:#888">Loading audit logs…</p>
    <div v-if="!aLoading" style="font-size:0.85em;color:#555;margin-bottom:8px">{{ auditTotal }} event(s)</div>

    <table v-if="auditLogs.length" style="width:100%;border-collapse:collapse;background:#fff;border-radius:8px;overflow:hidden;box-shadow:0 1px 4px rgba(0,0,0,.08);font-size:0.85em">
      <thead style="background:#f5f5f5">
        <tr>
          <th v-for="h in aHeaders" :key="h" style="padding:10px 12px;text-align:left;font-weight:600;color:#555">{{ h }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="log in auditLogs" :key="log.id" style="border-top:1px solid #eee">
          <td style="padding:8px 12px;font-weight:600;color:#1a1a2e">{{ log.action }}</td>
          <td style="padding:8px 12px;font-family:monospace">{{ log.showtimeId?.slice(-8) }}</td>
          <td style="padding:8px 12px;font-family:monospace">{{ log.seatId?.slice(-8) }}</td>
          <td style="padding:8px 12px;font-size:0.85em;color:#666;max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">
            {{ log.meta ? JSON.stringify(log.meta) : '' }}
          </td>
          <td style="padding:8px 12px;color:#666">{{ fmt(log.createdAt) }}</td>
        </tr>
      </tbody>
    </table>
    <p v-else-if="!aLoading" style="color:#888">No logs match this filter.</p>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import api from '../lib/api'

const bHeaders = ['Booking ID', 'Movie / Showtime', 'Seat', 'User', 'Booked At']
const aHeaders = ['Action', 'Showtime', 'Seat', 'Meta', 'At']
const auditActions = ['SEAT_LOCKED', 'BOOKING_SUCCESS', 'SEAT_RELEASED', 'BOOKING_TIMEOUT', 'LOCK_FAIL', 'BOOKING_DUPLICATE']

// Bookings
const bookings      = ref([])
const total         = ref(0)
const page          = ref(1)
const totalPages    = ref(1)
const bLoading      = ref(false)
const bError        = ref('')
const f             = ref({ movieTitle: '', date: '', userId: '' })
const showtimeMap   = ref({})  // showtimeId → movieTitle, loaded once on mount
const copied        = ref('')  // tracks which userId was last copied (for ✓ feedback)

// Audit
const auditLogs   = ref([])
const auditTotal  = ref(0)
const aLoading    = ref(false)
const auditAction = ref('')

let copiedTimer = null
function copyText(text) {
  navigator.clipboard.writeText(text).then(() => {
    copied.value = text
    clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => { copied.value = '' }, 1500)
  })
}

async function search() {
  page.value = 1
  await load()
}

async function load() {
  bLoading.value = true
  bError.value   = ''
  try {
    const params = { page: page.value, limit: 20 }
    if (f.value.movieTitle) params.movieTitle = f.value.movieTitle
    if (f.value.date)       params.date       = f.value.date
    if (f.value.userId)     params.userId     = f.value.userId
    const { data } = await api.get('/admin/bookings', { params })
    bookings.value   = data.bookings
    total.value      = data.total
    totalPages.value = data.totalPages
  } catch (e) {
    bError.value = e.response?.data?.error || 'Load failed'
  } finally {
    bLoading.value = false
  }
}

async function loadAudit() {
  aLoading.value = true
  try {
    const params = { limit: 20 }
    if (auditAction.value) params.action = auditAction.value
    const { data } = await api.get('/admin/audit-logs', { params })
    auditLogs.value  = data.logs
    auditTotal.value = data.total
  } catch {} finally {
    aLoading.value = false
  }
}

function clearF() {
  f.value = { movieTitle: '', date: '', userId: '' }
  page.value = 1
  load()
}

function prevPage() { if (page.value > 1) { page.value--; load() } }
function nextPage() { if (page.value < totalPages.value) { page.value++; load() } }

function fmt(iso) {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })
}

onMounted(async () => {
  // Load showtime titles once; used to enrich the bookings table
  try {
    const { data } = await api.get('/showtimes')
    const map = {}
    for (const st of data.showtimes ?? []) map[st.id] = st.movieTitle
    showtimeMap.value = map
  } catch {}
  load()
  loadAudit()
})
</script>
